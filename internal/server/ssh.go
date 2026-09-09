package server

import (
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"net"
	"net/http"
	"netconductor/internal/core"
	"os"
	"sync"
	"time"
)

type wsWriter struct {
	mu *sync.Mutex
	c  *websocket.Conn
}

func (w wsWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	e := w.c.WriteMessage(websocket.BinaryMessage, b)
	return len(b), e
}
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	d := s.device(r.PathValue("id"))
	if d == nil {
		s.mu.Unlock()
		core.Fail(w, 404, errors.New("设备不存在"))
		return
	}
	dev := *d
	s.mu.Unlock()
	if dev.SSHUser == "" || dev.SSHFingerprint == "" {
		core.Fail(w, 400, errors.New("请先配置 SSH 用户和已核对的主机指纹"))
		return
	}
	b, e := os.ReadFile(s.cfg.SSHKey)
	if e != nil {
		core.Fail(w, 503, errors.New("服务端未配置 SSH 专用密钥"))
		return
	}
	key, e := ssh.ParsePrivateKey(b)
	if e != nil {
		core.Fail(w, 503, errors.New("SSH 密钥不可用"))
		return
	}
	cfg := &ssh.ClientConfig{User: dev.SSHUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}, HostKeyCallback: func(host string, addr net.Addr, k ssh.PublicKey) error {
		if !core.Equal(ssh.FingerprintSHA256(k), dev.SSHFingerprint) {
			return errors.New("SSH 主机指纹不匹配")
		}
		return nil
	}, Timeout: 8 * time.Second}
	addr := core.HostPort(dev.IP, dev.SSHPort)
	raw, e := net.DialTimeout("tcp", addr, 8*time.Second)
	if e != nil {
		core.Fail(w, 502, errors.New("SSH 端口连接失败"))
		return
	}
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	cc, ch, req, e := ssh.NewClientConn(raw, addr, cfg)
	if e != nil {
		raw.Close()
		log.Printf("SSH connection rejected for device %s: %v", dev.ID, e)
		core.Fail(w, 502, errors.New("SSH 认证或主机指纹校验失败"))
		return
	}
	raw.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, ch, req)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		core.Fail(w, 502, e)
		return
	}
	defer session.Close()
	stdin, e := session.StdinPipe()
	if e != nil {
		core.Fail(w, 502, e)
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: core.SameOrigin, HandshakeTimeout: 5 * time.Second}
	ws, e := upgrader.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(65536)
	var mu sync.Mutex
	writer := wsWriter{&mu, ws}
	session.Stdout = writer
	session.Stderr = writer
	if e = session.RequestPty("xterm-256color", 30, 110, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); e != nil {
		writer.Write([]byte("终端分配失败\r\n"))
		return
	}
	if e = session.Shell(); e != nil {
		writer.Write([]byte("SSH shell 启动失败\r\n"))
		return
	}
	s.mu.Lock()
	s.audit("ssh.open", dev.Name)
	s.persist()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.audit("ssh.close", dev.Name); s.persist(); s.mu.Unlock() }()
	go func() { session.Wait(); ws.Close() }()
	timeout := time.AfterFunc(2*time.Hour, func() { client.Close(); ws.Close() })
	defer timeout.Stop()
	for {
		kind, b, e := ws.ReadMessage()
		if e != nil {
			return
		}
		if kind == websocket.BinaryMessage {
			if _, e = stdin.Write(b); e != nil {
				return
			}
		} else {
			var v struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(b, &v) == nil && v.Cols >= 10 && v.Cols <= 500 && v.Rows >= 5 && v.Rows <= 200 {
				session.WindowChange(v.Rows, v.Cols)
			}
		}
	}
}

var _ io.Writer = wsWriter{}
