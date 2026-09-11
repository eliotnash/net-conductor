package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/url"
	"netconductor/internal/core"
	"netconductor/internal/remote"
	"strconv"
	"strings"
	"sync"
	"time"
)

func localSSHAddress(c core.AgentConfig) (string, error) {
	address := c.SSHAddress
	if address == "" {
		address = "127.0.0.1:22"
	}
	host, port, err := net.SplitHostPort(address)
	n, parseErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || parseErr != nil || !core.Port(n) || ip == nil || (!ip.IsLoopback() && host != c.IP) {
		return "", errors.New("SSH address must be loopback or this device's tunnel IP")
	}
	return address, nil
}
func (a *Agent) remoteStatus(status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remoteState != status {
		a.remoteState = status
		a.event("remote.signaling", status)
	}
}
func (a *Agent) remoteLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		a.remoteConnect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
func (a *Agent) remoteConnect(ctx context.Context) {
	c := a.Config()
	if c.ID == "" {
		return
	}
	a.remoteStatus("connecting")
	defer a.remoteStatus("disconnected")
	a.syncSSHKey(ctx)
	endpoint := strings.Replace(c.Server, "https://", "wss://", 1) + "/api/remote/agent?id=" + url.QueryEscape(c.ID)
	client, err := makeClient(c.Certificate)
	if err != nil {
		return
	}
	dialer := websocket.Dialer{TLSClientConfig: client.Transport.(*http.Transport).TLSClientConfig, HandshakeTimeout: 10 * time.Second}
	ws, _, err := dialer.DialContext(ctx, endpoint, http.Header{"Authorization": []string{"Bearer " + c.Token}})
	if err != nil {
		return
	}
	defer ws.Close()
	a.remoteStatus("connected")
	ws.SetReadLimit(128 * 1024)
	connectionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writeMu sync.Mutex
	send := func(sid, kind string, v any) error {
		b, _ := json.Marshal(v)
		writeMu.Lock()
		defer writeMu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteJSON(remote.Message{SID: sid, Type: kind, Data: b})
	}
	go func() {
		timer := time.NewTicker(25 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-connectionCtx.Done():
				ws.Close()
				return
			case <-timer.C:
				if send("", "ping", nil) != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	sessions := map[string]*remote.Session{}
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()
	for {
		ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		var m remote.Message
		if ws.ReadJSON(&m) != nil {
			return
		}
		switch m.Type {
		case "offer":
			if sessions[m.SID] != nil || len(sessions) >= 8 {
				send(m.SID, "error", "Too many remote sessions")
				continue
			}
			var offer remote.Offer
			if json.Unmarshal(m.Data, &offer) != nil {
				continue
			}
			sid := m.SID
			address, addressErr := localSSHAddress(c)
			if addressErr != nil {
				send(sid, "error", addressErr.Error())
				continue
			}
			session, err := remote.New(connectionCtx, remote.Config{ExcludeIP: c.IP, Address: address, User: c.SSHUser, Key: c.SSHKey, Fingerprint: c.SSHHostFingerprint, Desktop: a.desktopRequest}, offer, func(kind string, v any) error { return send(sid, kind, v) })
			if err != nil {
				send(sid, "error", err.Error())
				continue
			}
			sessions[sid] = session
			a.mu.Lock()
			a.event("remote.open", "管理员已连接："+offer.Mode)
			a.mu.Unlock()
		case "relay":
			if s := sessions[m.SID]; s != nil {
				s.Relay(m.Data)
			}
		case "close":
			if s := sessions[m.SID]; s != nil {
				s.Close()
				delete(sessions, m.SID)
			}
		}
	}
}
func (a *Agent) syncSSHKey(ctx context.Context) {
	c := a.Config()
	if c.SSHAuthorizedKeys == "" {
		return
	}
	client, err := makeClient(c.Certificate)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Server+"/api/agent/ssh-key?id="+url.QueryEscape(c.ID), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var result struct {
		PublicKey string `json:"publicKey"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&result) != nil {
		return
	}
	a.op.Lock()
	defer a.op.Unlock()
	if remote.Authorize(c.SSHAuthorizedKeys, []byte(result.PublicKey)) != nil {
		a.mu.Lock()
		a.event("ssh.authorization.failed", "无法同步服务端 SSH 公钥")
		a.mu.Unlock()
	}
}

type desktopJob struct {
	ID      string         `json:"id"`
	Request remote.Request `json:"request"`
}
type desktopResult struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}
type desktopBridge struct {
	mu      sync.Mutex
	queue   chan desktopJob
	pending map[string]chan desktopResult
}

func (a *Agent) desktopRequest(ctx context.Context, r remote.Request) (any, error) {
	if a.desktop.queue == nil {
		return nil, errors.New("desktop unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	id := core.Token()
	result := make(chan desktopResult, 1)
	a.desktop.mu.Lock()
	a.desktop.pending[id] = result
	a.desktop.mu.Unlock()
	defer func() { a.desktop.mu.Lock(); delete(a.desktop.pending, id); a.desktop.mu.Unlock() }()
	select {
	case a.desktop.queue <- desktopJob{ID: id, Request: r}:
	case <-ctx.Done():
		return nil, errors.New("Windows desktop client not available")
	}
	select {
	case reply := <-result:
		if reply.Error != "" {
			return nil, errors.New(reply.Error)
		}
		return reply.Result, nil
	case <-ctx.Done():
		return nil, errors.New("Windows desktop is not logged in or client is not running")
	}
}
func (a *Agent) desktopNext(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.desktopSeen = time.Now()
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	for {
		select {
		case job := <-a.desktop.queue:
			a.desktop.mu.Lock()
			_, ok := a.desktop.pending[job.ID]
			a.desktop.mu.Unlock()
			if ok {
				core.JSON(w, job)
				return
			}
		case <-ctx.Done():
			core.JSON(w, map[string]any{"id": ""})
			return
		}
	}
}
func (a *Agent) desktopReply(w http.ResponseWriter, r *http.Request) {
	var result desktopResult
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024)).Decode(&result) != nil {
		http.Error(w, "invalid reply", 400)
		return
	}
	a.desktop.mu.Lock()
	ch := a.desktop.pending[result.ID]
	a.desktop.mu.Unlock()
	if ch != nil {
		select {
		case ch <- result:
		default:
		}
	}
	core.JSON(w, map[string]bool{"ok": true})
}
