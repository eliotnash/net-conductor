package server

import (
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"net/http"
	"netconductor/internal/core"
	"netconductor/internal/remote"
	"os"
	"sync"
	"time"
)

type signalConn struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (c *signalConn) send(m remote.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(m)
}

type remoteBrowser struct {
	conn   *signalConn
	device string
}
type remoteHub struct {
	sync.Mutex
	agents   map[string]*signalConn
	browsers map[string]remoteBrowser
}

func (s *Server) remoteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.Lock()
	d := s.device(id)
	ok := d != nil && core.Equal(core.Bearer(r), d.Token)
	s.mu.Unlock()
	if !ok || id == "cloud" {
		core.Fail(w, 401, errors.New("device authentication failed"))
		return
	}
	ws, err := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }, HandshakeTimeout: 5 * time.Second}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &signalConn{ws: ws}
	ws.SetReadLimit(1024 * 1024)
	s.remote.Lock()
	old := s.remote.agents[id]
	s.remote.agents[id] = c
	s.remote.Unlock()
	if old != nil {
		old.ws.Close()
	}
	defer func() {
		ws.Close()
		s.remote.Lock()
		if s.remote.agents[id] == c {
			delete(s.remote.agents, id)
			for _, b := range s.remote.browsers {
				if b.device == id {
					b.conn.ws.Close()
				}
			}
		}
		s.remote.Unlock()
	}()
	for {
		ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		var m remote.Message
		if ws.ReadJSON(&m) != nil {
			return
		}
		s.mu.Lock()
		current := s.device(id)
		valid := current != nil && core.Equal(core.Bearer(r), current.Token)
		s.mu.Unlock()
		if !valid {
			return
		}
		if m.Type == "ping" {
			c.send(remote.Message{Type: "pong"})
			continue
		}
		if m.Type != "answer" && m.Type != "relay" && m.Type != "error" {
			continue
		}
		s.remote.Lock()
		b, found := s.remote.browsers[m.SID]
		s.remote.Unlock()
		if found && b.device == id {
			if b.conn.send(m) != nil {
				b.conn.ws.Close()
			}
		}
	}
}
func (s *Server) remoteBrowser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mode := r.URL.Query().Get("mode")
	if mode != "ssh" && mode != "files" && mode != "desktop" {
		core.Fail(w, 400, errors.New("invalid mode"))
		return
	}
	s.mu.Lock()
	d := s.device(id)
	if d == nil {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	dev := *d
	s.mu.Unlock()
	if mode == "desktop" && dev.OS != "windows" {
		core.Fail(w, 400, errors.New("Windows desktop only"))
		return
	}
	s.remote.Lock()
	agent := s.remote.agents[id]
	count := len(s.remote.browsers)
	s.remote.Unlock()
	if count >= 32 {
		core.Fail(w, 429, errors.New("too many sessions"))
		return
	}
	if id != "cloud" && agent == nil {
		core.Fail(w, 503, errors.New("device client is offline or needs upgrading"))
		return
	}
	ws, err := (&websocket.Upgrader{CheckOrigin: core.SameOrigin, HandshakeTimeout: 5 * time.Second}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(128 * 1024)
	c := &signalConn{ws: ws}
	sid := core.Token()
	s.remote.Lock()
	s.remote.browsers[sid] = remoteBrowser{conn: c, device: id}
	s.remote.Unlock()
	s.mu.Lock()
	s.audit("remote.open", dev.Name+" / "+mode)
	s.persist()
	s.mu.Unlock()
	var local *remote.Session
	defer func() {
		if local != nil {
			local.Close()
		}
		if agent != nil {
			agent.send(remote.Message{SID: sid, Type: "close"})
		}
		s.remote.Lock()
		delete(s.remote.browsers, sid)
		s.remote.Unlock()
		s.mu.Lock()
		s.audit("remote.close", dev.Name+" / "+mode)
		s.persist()
		s.mu.Unlock()
	}()
	timeout := time.AfterFunc(30*time.Minute, func() { ws.Close() })
	defer timeout.Stop()
	offered := false
	for {
		ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		var m remote.Message
		if ws.ReadJSON(&m) != nil {
			return
		}
		if !s.admin(r) {
			return
		}
		m.SID = sid
		switch m.Type {
		case "offer":
			if offered {
				return
			}
			offered = true
			var offer remote.Offer
			if json.Unmarshal(m.Data, &offer) != nil {
				return
			}
			offer.Mode = mode
			m.Data, _ = json.Marshal(offer)
			if id == "cloud" {
				local, err = remote.New(r.Context(), remote.Config{Address: core.HostPort(dev.IP, dev.SSHPort), User: dev.SSHUser, Key: s.cfg.SSHKey, Fingerprint: dev.SSHFingerprint}, offer, func(kind string, v any) error {
					b, _ := json.Marshal(v)
					return c.send(remote.Message{Type: kind, Data: b})
				})
				if err != nil {
					b, _ := json.Marshal(err.Error())
					c.send(remote.Message{Type: "error", Data: b})
					return
				}
			} else if agent.send(m) != nil {
				return
			}
		case "relay":
			if !offered {
				return
			}
			if local != nil {
				local.Relay(m.Data)
			} else if agent.send(m) != nil {
				return
			}
		case "ping":
			c.send(remote.Message{Type: "pong"})
		case "close":
			return
		default:
			return
		}
	}
}
func (s *Server) agentSSHKey(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.mu.Lock()
	d := s.device(id)
	ok := d != nil && core.Equal(core.Bearer(r), d.Token)
	s.mu.Unlock()
	if !ok {
		core.Fail(w, 401, errors.New("unauthorized"))
		return
	}
	b, err := os.ReadFile(s.cfg.SSHKey)
	if err != nil {
		core.Fail(w, 503, errors.New("SSH key unavailable"))
		return
	}
	key, err := ssh.ParsePrivateKey(b)
	if err != nil {
		core.Fail(w, 503, errors.New("SSH key invalid"))
		return
	}
	core.JSON(w, map[string]string{"publicKey": string(ssh.MarshalAuthorizedKey(key.PublicKey()))})
}
