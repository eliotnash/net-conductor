package remote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/pion/webrtc/v4"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type Message struct {
	SID  string          `json:"sid,omitempty"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}
type Offer struct {
	Transport string                    `json:"transport,omitempty"`
	SDP       webrtc.SessionDescription `json:"sdp"`
	Mode      string                    `json:"mode"`
}
type Request struct {
	ID     int             `json:"id"`
	Op     string          `json:"op"`
	Path   string          `json:"path"`
	Target string          `json:"target"`
	Data   string          `json:"data"`
	Offset int64           `json:"offset"`
	Cols   int             `json:"cols"`
	Rows   int             `json:"rows"`
	Input  json.RawMessage `json:"input"`
}
type Config struct {
	ExcludeIP                       string
	Address, User, Key, Fingerprint string
	Desktop                         func(context.Context, Request) (any, error)
}
type Session struct {
	mu       sync.Mutex
	sendMu   sync.Mutex
	pc       *webrtc.PeerConnection
	dc       *webrtc.DataChannel
	relay    bool
	ctx      context.Context
	cancel   context.CancelFunc
	signal   func(string, any) error
	cfg      Config
	mode     string
	client   *ssh.Client
	files    *sftp.Client
	terminal *ssh.Session
	stdin    io.WriteCloser
	queue    chan []byte
}

func New(ctx context.Context, cfg Config, offer Offer, signal func(string, any) error) (*Session, error) {
	if offer.Mode != "ssh" && offer.Mode != "files" && offer.Mode != "desktop" {
		return nil, errors.New("invalid remote mode")
	}
	if offer.Mode == "desktop" && cfg.Desktop == nil {
		return nil, errors.New("remote desktop requires a logged-in Windows client")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	s := &Session{ctx: ctx, cancel: cancel, cfg: cfg, mode: offer.Mode, signal: signal, queue: make(chan []byte, 64)}
	if offer.Transport != "" && offer.Transport != "auto" && offer.Transport != "p2p" && offer.Transport != "relay" {
		cancel()
		return nil, errors.New("invalid transport")
	}
	if offer.Transport == "relay" {
		s.relay = true
		go s.run()
		return s, nil
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.String() != cfg.ExcludeIP })
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.cloudflare.com:3478"}}}})
	if err != nil {
		cancel()
		return nil, err
	}
	s.pc = pc
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		s.mu.Lock()
		if s.dc != nil || dc.Label() != "nc" {
			s.mu.Unlock()
			dc.Close()
			return
		}
		s.dc = dc
		s.mu.Unlock()
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			s.mu.Lock()
			relay := s.relay
			s.mu.Unlock()
			if !relay {
				s.accept(m.Data)
			}
		})
	})
	if err = pc.SetRemoteDescription(offer.SDP); err != nil {
		s.Close()
		return nil, err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.Close()
		return nil, err
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		s.Close()
		return nil, err
	}
	go func() {
		select {
		case <-gather:
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
		signal("answer", pc.LocalDescription())
	}()
	go s.run()
	go func() { <-ctx.Done(); s.Close() }()
	return s, nil
}
func (s *Session) Relay(data []byte) { s.mu.Lock(); s.relay = true; s.mu.Unlock(); s.accept(data) }
func (s *Session) accept(data []byte) {
	if len(data) > 65536 {
		s.cancel()
		return
	}
	b := append([]byte(nil), data...)
	select {
	case s.queue <- b:
	case <-s.ctx.Done():
	default:
		s.cancel()
	}
}
func (s *Session) Close() {
	s.cancel()
	s.mu.Lock()
	pc := s.pc
	s.pc = nil
	s.mu.Unlock()
	if pc != nil {
		pc.Close()
	}
}
func (s *Session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// Keep every data-channel message below 48 KiB, including desktop frames.
	if len(b) > 40000 {
		id := time.Now().UnixNano()
		for pos := 0; pos < len(b); pos += 24000 {
			end := pos + 24000
			if end > len(b) {
				end = len(b)
			}
			part, _ := json.Marshal(map[string]any{"type": "chunk", "chunk": id, "data": base64.StdEncoding.EncodeToString(b[pos:end]), "last": end == len(b)})
			if err = s.sendBytes(part); err != nil {
				return err
			}
		}
		return nil
	}
	return s.sendBytes(b)
}
func (s *Session) sendBytes(b []byte) error {
	s.mu.Lock()
	dc, relay := s.dc, s.relay
	s.mu.Unlock()
	if !relay && dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
		deadline := time.Now().Add(5 * time.Second)
		for dc.BufferedAmount() > 1024*1024 {
			select {
			case <-s.ctx.Done():
				return s.ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				return errors.New("remote peer too slow")
			}
		}
		return dc.SendText(string(b))
	}
	return s.signal("relay", json.RawMessage(b))
}
func (s *Session) connect() error {
	if s.client != nil {
		return nil
	}
	keyBytes, err := os.ReadFile(s.cfg.Key)
	if err != nil {
		return errors.New("SSH key unavailable")
	}
	key, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return err
	}
	raw, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(s.ctx, "tcp", s.cfg.Address)
	if err != nil {
		return err
	}
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	cfg := &ssh.ClientConfig{User: s.cfg.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}, HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if ssh.FingerprintSHA256(k) != s.cfg.Fingerprint {
			return errors.New("SSH host fingerprint mismatch")
		}
		return nil
	}}
	cc, ch, req, err := ssh.NewClientConn(raw, s.cfg.Address, cfg)
	if err != nil {
		raw.Close()
		return err
	}
	raw.SetDeadline(time.Time{})
	s.client = ssh.NewClient(cc, ch, req)
	go func(c *ssh.Client) { <-s.ctx.Done(); c.Close() }(s.client)
	return nil
}
func (s *Session) run() {
	defer func() {
		if s.files != nil {
			s.files.Close()
		}
		if s.terminal != nil {
			s.terminal.Close()
		}
		if s.client != nil {
			s.client.Close()
		}
	}()
	for {
		select {
		case <-s.ctx.Done():
			return
		case b := <-s.queue:
			var r Request
			if json.Unmarshal(b, &r) != nil {
				s.cancel()
				return
			}
			result, err := s.handle(r)
			response := map[string]any{"type": "result", "id": r.ID, "ok": err == nil, "result": result}
			if err != nil {
				response["error"] = err.Error()
			}
			if s.send(response) != nil {
				s.cancel()
				return
			}
		}
	}
}
func (s *Session) handle(r Request) (any, error) {
	if r.Op == "ping" {
		return "ready", nil
	}
	if s.mode == "desktop" {
		if r.Op != "frame" && r.Op != "input" {
			return nil, errors.New("operation not allowed")
		}
		return s.cfg.Desktop(s.ctx, r)
	}
	if err := s.connect(); err != nil {
		return nil, err
	}
	if s.mode == "files" {
		return s.fileRequest(r)
	}
	switch r.Op {
	case "terminal":
		if s.terminal != nil {
			return nil, errors.New("terminal already open")
		}
		term, err := s.client.NewSession()
		if err != nil {
			return nil, err
		}
		s.terminal = term
		s.stdin, err = term.StdinPipe()
		if err != nil {
			return nil, err
		}
		term.Stdout = terminalWriter{s}
		term.Stderr = terminalWriter{s}
		if err = term.RequestPty("xterm-256color", 30, 110, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return nil, err
		}
		if err = term.Shell(); err != nil {
			return nil, err
		}
		go func() { term.Wait(); s.send(map[string]any{"type": "terminalEnd"}) }()
		return "started", nil
	case "stdin":
		if s.stdin == nil {
			return nil, errors.New("terminal not open")
		}
		b, err := base64.StdEncoding.DecodeString(r.Data)
		if err != nil {
			return nil, err
		}
		_, err = s.stdin.Write(b)
		return nil, err
	case "resize":
		if s.terminal == nil || r.Cols < 10 || r.Cols > 500 || r.Rows < 5 || r.Rows > 200 {
			return nil, errors.New("invalid terminal size")
		}
		return nil, s.terminal.WindowChange(r.Rows, r.Cols)
	}
	return nil, errors.New("operation not allowed")
}

type terminalWriter struct{ s *Session }

func (w terminalWriter) Write(b []byte) (int, error) {
	for pos := 0; pos < len(b); pos += 16000 {
		end := pos + 16000
		if end > len(b) {
			end = len(b)
		}
		if err := w.s.send(map[string]any{"type": "terminal", "data": base64.StdEncoding.EncodeToString(b[pos:end])}); err != nil {
			return pos, err
		}
	}
	return len(b), nil
}
func (s *Session) fileRequest(r Request) (any, error) {
	if len(r.Path) > 4096 || len(r.Target) > 4096 || r.Offset < 0 || r.Offset > 1024*1024*1024*1024 {
		return nil, errors.New("invalid file request")
	}
	if s.files == nil {
		f, err := sftp.NewClient(s.client)
		if err != nil {
			return nil, err
		}
		s.files = f
	}
	f := s.files
	switch r.Op {
	case "list":
		p := r.Path
		if p == "" {
			var err error
			p, err = f.Getwd()
			if err != nil {
				return nil, err
			}
		}
		entries, err := f.ReadDir(p)
		if err != nil {
			return nil, err
		}
		if len(entries) > 10000 {
			return nil, errors.New("directory has more than 10000 entries")
		}
		rows := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, map[string]any{"name": e.Name(), "directory": e.IsDir(), "size": e.Size(), "modified": e.ModTime()})
		}
		return map[string]any{"path": p, "entries": rows}, nil
	case "read":
		file, err := f.Open(r.Path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		b := make([]byte, 24000)
		n, err := file.ReadAt(b, r.Offset)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return map[string]any{"data": base64.StdEncoding.EncodeToString(b[:n]), "eof": err == io.EOF}, nil
	case "create":
		file, err := f.OpenFile(r.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return nil, err
		}
		return nil, file.Close()
	case "write":
		b, err := base64.StdEncoding.DecodeString(r.Data)
		if err != nil || len(b) > 24000 {
			return nil, errors.New("invalid upload chunk")
		}
		file, err := f.OpenFile(r.Path, os.O_WRONLY)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		n, err := file.WriteAt(b, r.Offset)
		return n, err
	case "mkdir":
		return nil, f.Mkdir(r.Path)
	case "rename":
		if _, err := f.Lstat(r.Target); err == nil {
			return nil, errors.New("destination already exists")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		return nil, f.Rename(r.Path, r.Target)
	case "delete":
		stat, err := f.Lstat(r.Path)
		if err != nil {
			return nil, err
		}
		if stat.IsDir() {
			return nil, f.RemoveDirectory(r.Path)
		}
		return nil, f.Remove(r.Path)
	}
	return nil, errors.New("unknown file operation")
}
