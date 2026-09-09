package agent

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"netconductor/internal/core"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Agent struct {
	mu        sync.Mutex
	op        sync.Mutex
	cfg       core.AgentConfig
	path      string
	status    string
	lastError string
	lastSync  time.Time
	latency   int64
	checks    []core.Check
	events    []core.Event
	policy    core.Policy
	client    *http.Client
	shares    map[int]net.Listener
	rx        int64
	tx        int64
	handshake int64
	exitName  string
}

func New(path string) (*Agent, error) {
	var c core.AgentConfig
	if e := core.Load(path, &c); e != nil {
		return nil, e
	}
	if c.LocalListen == "" {
		c.LocalListen = "127.0.0.1:18765"
	}
	if c.ProxyListen == "" {
		c.ProxyListen = "127.0.0.1:17891"
	}
	if c.LocalToken == "" {
		c.LocalToken = core.Token()
	}
	if c.Interface == "" {
		c.Interface = "nc-wg"
	}
	if !core.SafeName(c.Interface) {
		return nil, errors.New("invalid interface")
	}
	for _, addr := range []string{c.LocalListen, c.ProxyListen} {
		h, _, e := net.SplitHostPort(addr)
		if e != nil || h != "127.0.0.1" {
			return nil, errors.New("agent local listeners must bind 127.0.0.1")
		}
	}
	if e := core.Save(path, c); e != nil {
		return nil, e
	}
	a := &Agent{cfg: c, path: path, status: "disconnected", shares: map[int]net.Listener{}}
	client, e := makeClient(c.Certificate)
	if e != nil {
		return nil, e
	}
	a.client = client
	return a, nil
}
func makeClient(cert string) (*http.Client, error) {
	roots, e := x509.SystemCertPool()
	if e != nil {
		roots = x509.NewCertPool()
	}
	if cert != "" && !roots.AppendCertsFromPEM([]byte(cert)) {
		return nil, errors.New("服务器证书格式错误")
	}
	return &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, Proxy: nil}, CheckRedirect: func(r *http.Request, via []*http.Request) error { return errors.New("redirect denied") }}, nil
}
func (a *Agent) Config() core.AgentConfig { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg }
func (a *Agent) event(action, detail string) {
	a.events = append(a.events, core.Event{At: time.Now(), Action: action, Detail: detail})
	if len(a.events) > 100 {
		a.events = a.events[len(a.events)-100:]
	}
}
func (a *Agent) Handler(assets fs.FS) http.Handler {
	localListen := a.Config().LocalListen
	m := http.NewServeMux()
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		ok := core.Equal(core.Bearer(r), a.cfg.LocalToken)
		a.mu.Unlock()
		if !ok {
			core.Fail(w, 401, errors.New("本机客户端认证失败"))
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/local/state":
			a.state(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/local/join":
			a.join(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/local/diagnose":
			a.diagnose()
			a.state(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/local/repair":
			a.repair(w, r)
		case r.Method == "POST" && r.URL.Path == "/api/local/share":
			a.share(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	m.Handle("/", http.FileServer(http.FS(assets)))
	return core.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != localListen {
			core.Fail(w, 403, errors.New("invalid host"))
			return
		}
		m.ServeHTTP(w, r)
	}))
}
func (a *Agent) state(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	core.JSON(w, map[string]any{"mode": "agent", "version": core.Version, "name": a.cfg.Name, "ip": a.cfg.IP, "server": a.cfg.Server, "interface": a.cfg.Interface, "proxyListen": a.cfg.ProxyListen, "status": a.status, "lastError": a.lastError, "lastSync": a.lastSync, "latencyMs": a.latency, "checks": a.checks, "events": a.events, "policy": a.policy, "enrolled": a.cfg.ID != "", "rx": a.rx, "tx": a.tx, "handshake": a.handshake, "exitName": a.exitName, "shares": a.cfg.Shares, "tunnelReady": a.status == "connected" && a.handshake > 0 && time.Since(time.Unix(a.handshake, 0)) < 180*time.Second})
}
func (a *Agent) Run(ctx context.Context) {
	a.restoreShares()
	a.diagnose()
	a.sync(ctx)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			a.op.Lock()
			for _, ln := range a.shares {
				ln.Close()
			}
			a.op.Unlock()
			return
		case <-t.C:
			n++
			if n%3 == 0 {
				a.restoreShares()
				a.diagnose()
			}
			a.sync(ctx)
		}
	}
}
func (a *Agent) sync(ctx context.Context) {
	a.mu.Lock()
	c := a.cfg
	latency := a.latency
	checks := a.checks
	client := a.client
	a.mu.Unlock()
	if c.ID == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"id": c.ID, "version": core.Version, "latencyMs": latency, "diagnostics": checks})
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Server, "/")+"/api/heartbeat", bytes.NewReader(body))
	if e != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, e := client.Do(req)
	var data struct {
		Policy    core.Policy `json:"policy"`
		RX        int64       `json:"rx"`
		TX        int64       `json:"tx"`
		Handshake int64       `json:"handshake"`
		ExitName  string      `json:"exitName"`
	}
	if e == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			e = fmt.Errorf("server returned %d", resp.StatusCode)
		} else {
			e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&data)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e != nil {
		if a.status != "disconnected" {
			a.event("connection.lost", "服务器连接断开，正在自动重试")
		}
		a.status = "disconnected"
		a.lastError = "服务器连接失败，请检查网络、证书和服务状态"
	} else {
		if a.status != "connected" {
			a.event("connection.ready", "服务器已连接")
		}
		a.status = "connected"
		a.lastError = ""
		a.lastSync = time.Now()
		a.latency = time.Since(start).Milliseconds()
		a.policy = data.Policy
		a.rx = data.RX
		a.tx = data.TX
		a.handshake = data.Handshake
		a.exitName = data.ExitName
	}
}
func (a *Agent) diagnose() {
	c := a.Config()
	var checks []core.Check
	if c.Server != "" {
		u, e := url.Parse(c.Server)
		if e == nil {
			host := u.Host
			if u.Port() == "" {
				host = net.JoinHostPort(u.Hostname(), "443")
			}
			checks = append(checks, core.TCPCheck(host))
		}
	}
	wg := filepath.Join(c.WireGuardPath, "wg.exe")
	if runtime.GOOS != "windows" {
		wg = "wg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, e := exec.CommandContext(ctx, wg, "show", c.Interface, "public-key").Output()
	checks = append(checks, core.Check{Name: "WireGuard 隧道", OK: e == nil, Detail: map[bool]string{true: "接口可用", false: "接口未启动或需要管理员权限"}[e == nil]})
	if c.IP != "" {
		found := false
		addrs, _ := net.InterfaceAddrs()
		for _, x := range addrs {
			ip, _, _ := net.ParseCIDR(x.String())
			if ip != nil && ip.String() == c.IP {
				found = true
			}
		}
		checks = append(checks, core.Check{Name: "虚拟 IP", OK: found, Detail: c.IP})
	}
	checks = append(checks, core.TCPCheck("127.0.0.1:22"), core.TCPCheck(c.ProxyListen))
	a.mu.Lock()
	a.checks = checks
	a.mu.Unlock()
}
func (a *Agent) ProxyHandler() http.Handler {
	return core.ProxyHandler(func(r *http.Request) (core.DialFunc, error) {
		return func(ctx context.Context, address string) (net.Conn, error) {
			c := a.Config()
			if c.ID == "" || c.HubProxy == "" {
				return nil, errors.New("not enrolled")
			}
			u := &url.URL{Scheme: "http", Host: c.HubProxy, User: url.UserPassword(c.ID, c.Token)}
			return core.DialVia(ctx, u.String(), address)
		}, nil
	})
}
func (a *Agent) join(w http.ResponseWriter, r *http.Request) {
	a.op.Lock()
	defer a.op.Unlock()
	var v struct {
		Server      string `json:"server"`
		Certificate string `json:"certificate"`
		Code        string `json:"code"`
		Name        string `json:"name"`
		Interface   string `json:"interface"`
		Adopt       bool   `json:"adopt"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	c := a.Config()
	if c.ID != "" {
		core.Fail(w, 409, errors.New("设备已入网"))
		return
	}
	u, e := url.Parse(v.Server)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !core.SafeName(v.Interface) {
		core.Fail(w, 400, errors.New("请填写 HTTPS 服务器地址和合法隧道名称"))
		return
	}
	client, e := makeClient(v.Certificate)
	if e != nil {
		core.Fail(w, 400, e)
		return
	}
	var pub string
	var private string
	if v.Adopt {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		b, e := exec.CommandContext(ctx, filepath.Join(c.WireGuardPath, "wg.exe"), "show", v.Interface, "public-key").Output()
		if e != nil {
			core.Fail(w, 400, errors.New("无法读取现有 WireGuard 隧道"))
			return
		}
		pub = strings.TrimSpace(string(b))
	} else {
		key, e := ecdh.X25519().GenerateKey(rand.Reader)
		if e != nil {
			core.Fail(w, 500, e)
			return
		}
		private = base64.StdEncoding.EncodeToString(key.Bytes())
		pub = base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	}
	body, _ := json.Marshal(map[string]string{"code": v.Code, "name": v.Name, "publicKey": pub})
	req, _ := http.NewRequestWithContext(r.Context(), "POST", strings.TrimRight(v.Server, "/")+"/api/join", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		core.Fail(w, 502, errors.New("无法连接服务器，请核对地址和证书"))
		return
	}
	defer resp.Body.Close()
	var result struct {
		Device          core.Device `json:"device"`
		ServerPublicKey string      `json:"serverPublicKey"`
		Endpoint        string      `json:"endpoint"`
		Network         string      `json:"network"`
		Proxy           string      `json:"proxy"`
		Adopted         bool        `json:"adopted"`
		Error           string      `json:"error"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result); e != nil || resp.StatusCode != 200 {
		core.Fail(w, 400, errors.New("入网失败："+result.Error))
		return
	}
	c.Server = strings.TrimRight(v.Server, "/")
	c.Certificate = v.Certificate
	c.ID = result.Device.ID
	c.IP = result.Device.IP
	c.Name = v.Name
	c.Token = result.Device.Token
	c.Interface = v.Interface
	c.HubProxy = result.Proxy
	c.PrivateKey = private
	if !v.Adopt {
		c.TunnelConfig = fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = 25\n", private, c.IP, result.ServerPublicKey, result.Endpoint, result.Network)
	}
	if e = core.Save(a.path, c); e != nil {
		core.Fail(w, 500, e)
		return
	}
	a.mu.Lock()
	a.cfg = c
	a.client = client
	a.event("device.join", "已加入网络")
	a.mu.Unlock()
	if !v.Adopt {
		if e = a.installTunnel(); e != nil {
			core.Fail(w, 500, errors.New("已登记设备，隧道安装失败；请使用修复功能重试"))
			return
		}
	}
	core.JSON(w, map[string]bool{"ok": true})
}
func (a *Agent) installTunnel() error {
	c := a.Config()
	if c.TunnelConfig == "" {
		return errors.New("no managed tunnel config")
	}
	path := filepath.Join(filepath.Dir(a.path), c.Interface+".conf")
	if e := os.WriteFile(path, []byte(c.TunnelConfig), 0600); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, filepath.Join(c.WireGuardPath, "wireguard.exe"), "/installtunnelservice", path).Run()
}
func (a *Agent) repair(w http.ResponseWriter, r *http.Request) {
	a.op.Lock()
	defer a.op.Unlock()
	c := a.Config()
	backup := filepath.Join(filepath.Dir(a.path), "backup-"+time.Now().Format("20060102-150405")+".json")
	if e := core.Save(backup, c); e != nil {
		core.Fail(w, 500, e)
		return
	}
	var e error
	if runtime.GOOS == "windows" {
		script := "Restart-Service -LiteralName 'WireGuardTunnel$" + c.Interface + "' -ErrorAction Stop" // Service cmdlet uses Name, interface is strictly validated.
		script = strings.Replace(script, "-LiteralName", "-Name", 1)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		e = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run()
		if e != nil && c.TunnelConfig != "" {
			e = a.installTunnel()
		}
	} else {
		e = errors.New("Windows only")
	}
	a.mu.Lock()
	if e != nil {
		a.event("repair.failed", "隧道恢复失败，请检查服务权限")
	} else {
		a.event("repair.ok", "配置已备份，隧道已重新连接")
	}
	a.mu.Unlock()
	if e != nil {
		core.Fail(w, 500, errors.New("隧道修复失败；原配置备份已保留"))
		return
	}
	a.diagnose()
	core.JSON(w, map[string]bool{"ok": true})
}
func (a *Agent) share(w http.ResponseWriter, r *http.Request) {
	a.op.Lock()
	defer a.op.Unlock()
	var v struct {
		Port       int `json:"port"`
		ListenPort int `json:"listenPort"`
	}
	if e := core.Decode(w, r, &v); e != nil || !core.Port(v.Port) || !core.Port(v.ListenPort) || v.ListenPort < 1024 || v.Port == 17891 || v.Port == 18765 {
		core.Fail(w, 400, errors.New("请选择实际代理端口"))
		return
	}
	c := a.Config()
	hub, _, e := net.SplitHostPort(c.HubProxy)
	if e != nil || net.ParseIP(hub) == nil || net.ParseIP(c.IP) == nil {
		core.Fail(w, 400, errors.New("请先入网"))
		return
	}
	if !core.TCPCheck(core.HostPort("127.0.0.1", v.Port)).OK {
		core.Fail(w, 400, errors.New("本机代理端口未启动"))
		return
	}
	share := core.SharedPort{LocalPort: v.Port, ListenPort: v.ListenPort}
	for _, x := range c.Shares {
		if x.ListenPort == v.ListenPort {
			if x.LocalPort != v.Port {
				core.Fail(w, 409, errors.New("共享端口已分配"))
				return
			}
			core.JSON(w, map[string]bool{"ok": true})
			return
		}
	}
	if e = core.Save(filepath.Join(filepath.Dir(a.path), "backup-share-"+time.Now().Format("20060102-150405")+".json"), c); e != nil {
		core.Fail(w, 500, e)
		return
	}
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$rule='NetConductor-Proxy-%d'
if (-not (Get-NetFirewallRule -Name $rule -ErrorAction SilentlyContinue)) { New-NetFirewallRule -Name $rule -DisplayName $rule -Direction Inbound -Action Allow -Protocol TCP -LocalAddress %s -LocalPort %d -RemoteAddress %s | Out-Null }
`, v.ListenPort, c.IP, v.ListenPort, hub)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if e = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run(); e != nil {
		core.Fail(w, 500, errors.New("共享配置失败，请检查后台服务权限"))
		return
	}
	ln, e := a.startShare(c, share)
	if e != nil {
		core.Fail(w, 409, errors.New("共享端口无法监听，请选择其他端口"))
		return
	}
	c.Shares = append(c.Shares, share)
	if e = core.Save(a.path, c); e != nil {
		ln.Close()
		core.Fail(w, 500, e)
		return
	}
	a.shares[v.ListenPort] = ln
	a.mu.Lock()
	a.cfg = c
	a.event("proxy.share", "已配置仅限服务器访问的代理端口")
	a.mu.Unlock()
	core.JSON(w, map[string]bool{"ok": true})
}

func (a *Agent) restoreShares() {
	a.op.Lock()
	defer a.op.Unlock()
	c := a.Config()
	for _, p := range c.Shares {
		if a.shares[p.ListenPort] == nil {
			ln, e := a.startShare(c, p)
			if e == nil {
				a.shares[p.ListenPort] = ln
			}
		}
	}
}
func (a *Agent) startShare(c core.AgentConfig, p core.SharedPort) (net.Listener, error) {
	hub, _, e := net.SplitHostPort(c.HubProxy)
	if e != nil {
		return nil, e
	}
	ln, e := net.Listen("tcp", core.HostPort(c.IP, p.ListenPort))
	if e != nil {
		return nil, e
	}
	go func() {
		limit := make(chan struct{}, 128)
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			if host != hub {
				conn.Close()
				continue
			}
			select {
			case limit <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			go func() {
				defer func() { <-limit }()
				local, e := net.DialTimeout("tcp", core.HostPort("127.0.0.1", p.LocalPort), 5*time.Second)
				if e != nil {
					conn.Close()
					return
				}
				core.Bridge(conn, local)
			}()
		}
	}()
	return ln, nil
}
