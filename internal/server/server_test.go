package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"netconductor/internal/core"
	"netconductor/web"
	"path/filepath"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, e := New(core.ServerConfig{AdminToken: "test-admin-token-at-least-24-characters", Interface: "wg0", ServerIP: "10.77.0.1", Network: "10.77.0.0/24", MapBind: "127.0.0.1"}, filepath.Join(t.TempDir(), "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func request(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	assets, _ := fs.Sub(web.Files, "static")
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler(assets).ServeHTTP(w, r)
	return w
}
func TestAuthAndSecretRedaction(t *testing.T) {
	s := testServer(t)
	if w := request(s, "GET", "/api/state", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated state: %d", w.Code)
	}
	w := request(s, "GET", "/api/state", "", s.cfg.AdminToken)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(s.state.Devices[0].Token)) {
		t.Fatal("device token leaked")
	}
	var payload map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &payload); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"devices", "policies", "mappings", "events"} {
		if _, ok := payload[name].([]any); !ok {
			t.Fatalf("%s must be an array, including when empty", name)
		}
	}
	r := httptest.NewRequest("PUT", "http://local/api/devices/cloud", bytes.NewBufferString("{}"))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Authorization", "Bearer "+s.cfg.AdminToken)
	out := httptest.NewRecorder()
	assets, _ := fs.Sub(web.Files, "static")
	s.Handler(assets).ServeHTTP(out, r)
	if out.Code != 403 {
		t.Fatal("cross-origin write allowed")
	}
}
func TestLoginCookieAndRateLimit(t *testing.T) {
	s := testServer(t)
	body, _ := json.Marshal(map[string]string{"password": s.cfg.AdminToken})
	w := request(s, "POST", "/api/login", string(body), "")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("login failed")
	}
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	for i := 0; i < 10; i++ {
		request(s, "POST", "/api/login", `{"password":"wrong"}`, "")
	}
	if w = request(s, "POST", "/api/login", `{"password":"wrong"}`, ""); w.Code != 429 {
		t.Fatal("rate limit missing")
	}
}
func TestPolicyValidationPersistenceAndFailClosed(t *testing.T) {
	s := testServer(t)
	w := request(s, "PUT", "/api/policies/cloud", `{"source":"cloud","exits":["missing"],"onFailure":"block","active":""}`, s.cfg.AdminToken)
	if w.Code != 400 {
		t.Fatal("invalid exit accepted")
	}
	w = request(s, "PUT", "/api/policies/cloud", `{"source":"cloud","exits":[],"onFailure":"block","active":""}`, s.cfg.AdminToken)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, e := s.dialPolicy(context.Background(), "cloud", "127.0.0.1:80"); e == nil {
		t.Fatal("fail closed violated")
	}
	var st core.State
	if e := core.Load(s.path, &st); e != nil || len(st.Policies) != 1 {
		t.Fatal("policy not persisted")
	}
}
func TestProxyFailover(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "destination") }))
	defer echo.Close()
	up := httptest.NewServer(core.ProxyHandler(func(r *http.Request) (core.DialFunc, error) { return core.DialDirect, nil }))
	defer up.Close()
	host, p, _ := net.SplitHostPort(up.Listener.Addr().String())
	port := up.Listener.Addr().(*net.TCPAddr).Port
	_ = p
	s := testServer(t)
	s.state.Devices = append(s.state.Devices, core.Device{ID: "bad", IP: "127.0.0.1", ProxyPort: 1, ProxyType: "http"}, core.Device{ID: "good", IP: host, ProxyPort: port, ProxyType: "http"})
	s.state.Policies = []core.Policy{{Source: "cloud", Exits: []string{"bad", "good"}, OnFailure: "block"}}
	c, e := s.dialPolicy(context.Background(), "cloud", echo.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(c, "GET / HTTP/1.0\r\nHost: test\r\n\r\n")
	b, e := io.ReadAll(c)
	if e != nil || !bytes.Contains(b, []byte("destination")) {
		t.Fatalf("failed proxy data: %s %v", b, e)
	}
	if s.state.Policies[0].Active != "good" {
		t.Fatal("fallback not selected")
	}
}
func TestTCPAndUDPForwarding(t *testing.T) {
	s := testServer(t)
	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer tcp.Close()
	go func() {
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	udp, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, a, e := udp.ReadFromUDP(buf)
			if e != nil {
				return
			}
			udp.WriteToUDP(buf[:n], a)
		}
	}()
	s.state.Devices = append(s.state.Devices, core.Device{ID: "target", IP: "127.0.0.1"})
	for _, proto := range []string{"tcp", "udp"} {
		t.Run(proto, func(t *testing.T) {
			ln, _ := net.Listen("tcp", "127.0.0.1:0")
			p := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			target := tcp.Addr().(*net.TCPAddr).Port
			if proto == "udp" {
				target = udp.LocalAddr().(*net.UDPAddr).Port
			}
			stop, e := s.startMapping(core.Mapping{Device: "target", Protocol: proto, ListenPort: p, TargetPort: target})
			if e != nil {
				t.Fatal(e)
			}
			defer stop()
			c, e := net.Dial(proto, core.HostPort("127.0.0.1", p))
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			c.Write([]byte("hello"))
			buf := make([]byte, 5)
			if _, e = io.ReadFull(c, buf); e != nil || string(buf) != "hello" {
				t.Fatal("forward did not relay", e)
			}
		})
	}
}
func TestMappingRejectsUnknownDeviceAndProtectedPorts(t *testing.T) {
	s := testServer(t)
	for _, m := range []core.Mapping{{Name: "bad", Protocol: "tcp", ListenPort: 22, TargetPort: 80, Device: "cloud"}, {Name: "bad", Protocol: "tcp", ListenPort: 23456, TargetPort: 80, Device: "unknown"}} {
		if s.validateMapping(m) == nil {
			t.Fatal("unsafe mapping accepted")
		}
	}
}
func TestSSHRequiresConfiguredIdentity(t *testing.T) {
	s := testServer(t)
	if w := request(s, "GET", "/api/ssh/cloud", "", s.cfg.AdminToken); w.Code != 400 {
		t.Fatal("SSH accepted missing identity")
	}
}
