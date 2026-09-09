package server

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"netconductor/internal/core"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type enrollment struct {
	Expires time.Time
	IP      string
}
type Server struct {
	mu            sync.Mutex
	cfg           core.ServerConfig
	path          string
	state         core.State
	sessions      map[string]time.Time
	codes         map[string]enrollment
	loginAttempts map[string][]time.Time
	forwards      map[string]func()
	forwardMu     sync.Mutex
}

func New(cfg core.ServerConfig, path string) (*Server, error) {
	if !core.SafeName(cfg.Interface) || net.ParseIP(cfg.ServerIP) == nil || len(cfg.AdminToken) < 24 {
		return nil, errors.New("invalid server configuration")
	}
	s := &Server{cfg: cfg, path: path, sessions: map[string]time.Time{}, codes: map[string]enrollment{}, loginAttempts: map[string][]time.Time{}, forwards: map[string]func(){}}
	if e := core.Load(path, &s.state); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if len(s.state.Devices) == 0 {
		s.state.Devices = []core.Device{{ID: "cloud", Name: "公网服务器", IP: cfg.ServerIP, OS: "linux", Token: core.Token(), SSHPort: 22, Status: "online"}}
		if e := core.Save(path, s.state); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Server) persist() error { return core.Save(s.path, s.state) }
func (s *Server) audit(action, detail string) {
	s.state.Events = append(s.state.Events, core.Event{At: time.Now(), Action: action, Detail: detail})
	if len(s.state.Events) > 200 {
		s.state.Events = s.state.Events[len(s.state.Events)-200:]
	}
}
func (s *Server) device(id string) *core.Device {
	for i := range s.state.Devices {
		if s.state.Devices[i].ID == id {
			return &s.state.Devices[i]
		}
	}
	return nil
}
func (s *Server) admin(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if core.Equal(core.Bearer(r), s.cfg.AdminToken) {
		return true
	}
	c, e := r.Cookie("nc_session")
	if e != nil {
		return false
	}
	t, ok := s.sessions[c.Value]
	return ok && time.Now().Before(t)
}
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.admin(r) {
			core.Fail(w, 401, errors.New("请先登录"))
			return
		}
		h(w, r)
	}
}
func (s *Server) Handler(assets fs.FS) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if c, e := r.Cookie("nc_session"); e == nil {
			delete(s.sessions, c.Value)
		}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "nc_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		core.JSON(w, map[string]bool{"ok": true})
	})
	m.HandleFunc("GET /api/state", s.guard(s.getState))
	m.HandleFunc("POST /api/enrollment", s.guard(s.createCode))
	m.HandleFunc("POST /api/join", s.join)
	m.HandleFunc("POST /api/heartbeat", s.heartbeat)
	m.HandleFunc("PUT /api/devices/{id}", s.guard(s.updateDevice))
	m.HandleFunc("PUT /api/policies/{id}", s.guard(s.putPolicy))
	m.HandleFunc("POST /api/mappings", s.guard(s.putMapping))
	m.HandleFunc("DELETE /api/mappings/{id}", s.guard(s.deleteMapping))
	m.HandleFunc("GET /api/ssh/{id}", s.guard(s.terminal))
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		core.JSON(w, map[string]string{"version": core.Version, "status": "ok"})
	})
	m.Handle("/", http.FileServer(http.FS(assets)))
	return core.Headers(m)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Password string `json:"password"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.loginAttempts) > 4096 {
		for k, ts := range s.loginAttempts {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > time.Minute {
				delete(s.loginAttempts, k)
			}
		}
	}
	var recent []time.Time
	for _, t := range s.loginAttempts[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		core.Fail(w, 429, errors.New("尝试过于频繁，请稍后重试"))
		return
	}
	s.loginAttempts[ip] = append(recent, now)
	if !core.Equal(v.Password, s.cfg.AdminToken) {
		core.Fail(w, 401, errors.New("管理密码错误"))
		return
	}
	for k, t := range s.sessions {
		if now.After(t) {
			delete(s.sessions, k)
		}
	}
	token := core.Token()
	s.sessions[token] = now.Add(12 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "nc_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	core.JSON(w, map[string]bool{"ok": true})
}
func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state
	v.Policies = append([]core.Policy{}, s.state.Policies...)
	v.Mappings = append([]core.Mapping{}, s.state.Mappings...)
	v.Events = append([]core.Event{}, s.state.Events...)
	v.Devices = append([]core.Device{}, s.state.Devices...)
	for i := range v.Devices {
		v.Devices[i].Token = ""
		d := &v.Devices[i]
		if d.ID == "cloud" {
			d.Status = "online"
		} else if time.Since(d.LastSeen) > 20*time.Second {
			d.Status = "offline"
		} else {
			d.Status = "online"
		}
	}
	core.JSON(w, struct {
		core.State
		Version  string    `json:"version"`
		ServerIP string    `json:"serverIP"`
		Time     time.Time `json:"time"`
	}{v, core.Version, s.cfg.ServerIP, time.Now()})
}
func (s *Server) createCode(w http.ResponseWriter, r *http.Request) {
	var v struct {
		IP string `json:"ip"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	if v.IP != "" {
		_, network, e := net.ParseCIDR(s.cfg.Network)
		if e != nil || !network.Contains(net.ParseIP(v.IP)) || v.IP == s.cfg.ServerIP {
			core.Fail(w, 400, errors.New("地址必须属于私网且不能是服务器地址"))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.codes {
		if time.Now().After(v.Expires) {
			delete(s.codes, k)
		}
	}
	if len(s.codes) >= 100 {
		core.Fail(w, 429, errors.New("待使用入网码过多"))
		return
	}
	code := core.Token()
	s.codes[code] = enrollment{time.Now().Add(10 * time.Minute), v.IP}
	core.JSON(w, map[string]any{"code": code, "expiresIn": 600})
}
func runWG(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, "wg", args...).Output()
	return strings.TrimSpace(string(b)), e
}
func validKey(k string) bool {
	b, e := base64.StdEncoding.DecodeString(k)
	if e != nil || len(b) != 32 {
		return false
	}
	_, e = ecdh.X25519().NewPublicKey(b)
	return e == nil
}
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		PublicKey string `json:"publicKey"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	if len(v.Name) < 1 || len(v.Name) > 80 || !validKey(v.PublicKey) {
		core.Fail(w, 400, errors.New("设备名称或公钥不合法"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.codes[v.Code]
	if !ok || time.Now().After(code.Expires) {
		core.Fail(w, 403, errors.New("入网码无效或已过期"))
		return
	}
	for _, d := range s.state.Devices {
		if d.PublicKey == v.PublicKey || code.IP != "" && d.IP == code.IP {
			core.Fail(w, 409, errors.New("设备已存在，请使用原设备配置"))
			return
		}
	}
	key, e := runWG("show", s.cfg.Interface, "public-key")
	if e != nil {
		core.Fail(w, 503, errors.New("服务器 WireGuard 不可用"))
		return
	}
	existing, e := runWG("show", s.cfg.Interface, "allowed-ips")
	if e != nil {
		core.Fail(w, 503, e)
		return
	}
	ip := code.IP
	if ip == "" {
		base, network, e := net.ParseCIDR(s.cfg.Network)
		if e != nil || base.To4() == nil {
			core.Fail(w, 500, errors.New("仅支持 IPv4 私网"))
			return
		}
		b := base.To4()
		for n := 2; n < 255; n++ {
			candidate := net.IPv4(b[0], b[1], b[2], byte(n)).String()
			used := strings.Contains(existing, candidate+"/32") || candidate == s.cfg.ServerIP
			for _, d := range s.state.Devices {
				used = used || d.IP == candidate
			}
			if !used && network.Contains(net.ParseIP(candidate)) {
				ip = candidate
				break
			}
		}
	}
	if ip == "" {
		core.Fail(w, 409, errors.New("没有可用地址"))
		return
	}
	// Adoption must match the existing peer. Never overwrite another peer's route.
	adopted := false
	for _, line := range strings.Split(existing, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.Contains(f[1], ip+"/32") {
			if f[0] != v.PublicKey {
				core.Fail(w, 409, errors.New("现有 WireGuard 公钥不匹配"))
				return
			}
			adopted = true
		}
	}
	if !adopted {
		if _, e = runWG("set", s.cfg.Interface, "peer", v.PublicKey, "allowed-ips", ip+"/32"); e != nil {
			core.Fail(w, 500, errors.New("创建 WireGuard 节点失败"))
			return
		}
	}
	d := core.Device{ID: core.Token()[:12], Name: v.Name, IP: ip, PublicKey: v.PublicKey, Token: core.Token(), OS: "windows", SSHPort: 22, ProxyType: "socks5"}
	s.state.Devices = append(s.state.Devices, d)
	s.audit("device.join", d.Name)
	if e = s.persist(); e != nil {
		s.state.Devices = s.state.Devices[:len(s.state.Devices)-1]
		if !adopted {
			runWG("set", s.cfg.Interface, "peer", v.PublicKey, "remove")
		}
		core.Fail(w, 500, e)
		return
	}
	delete(s.codes, v.Code)
	core.JSON(w, map[string]any{"device": d, "serverPublicKey": key, "endpoint": s.cfg.PublicEndpoint, "network": s.cfg.Network, "proxy": s.cfg.ProxyListen, "adopted": adopted})
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ID          string       `json:"id"`
		Version     string       `json:"version"`
		LatencyMS   int64        `json:"latencyMs"`
		Diagnostics []core.Check `json:"diagnostics"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(v.ID)
	if d == nil || !core.Equal(d.Token, core.Bearer(r)) {
		core.Fail(w, 401, errors.New("设备认证失败"))
		return
	}
	if len(v.Diagnostics) > 20 {
		core.Fail(w, 400, errors.New("too many checks"))
		return
	}
	d.LastSeen = time.Now()
	d.LatencyMS = v.LatencyMS
	d.AgentVersion = v.Version
	d.Diagnostics = v.Diagnostics
	p := core.Policy{Source: d.ID, OnFailure: "block"}
	for _, x := range s.state.Policies {
		if x.Source == d.ID {
			p = x
		}
	}
	exitName := "暂无成功出口"
	if p.Active == "DIRECT" {
		exitName = "直接连接"
	} else if exit := s.device(p.Active); exit != nil {
		exitName = exit.Name
	}
	core.JSON(w, map[string]any{"policy": p, "time": time.Now(), "rx": d.TX, "tx": d.RX, "handshake": d.Handshake, "exitName": exitName})
}
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Name           string `json:"name"`
		ProxyPort      int    `json:"proxyPort"`
		ProxyType      string `json:"proxyType"`
		SSHUser        string `json:"sshUser"`
		SSHFingerprint string `json:"sshFingerprint"`
		SSHPort        int    `json:"sshPort"`
	}
	if e := core.Decode(w, r, &v); e != nil {
		core.Fail(w, 400, e)
		return
	}
	if len(v.Name) == 0 || len(v.Name) > 80 || v.ProxyPort != 0 && !core.Port(v.ProxyPort) || !core.Port(v.SSHPort) || v.ProxyType != "http" && v.ProxyType != "socks5" || len(v.SSHUser) > 80 || len(v.SSHFingerprint) > 100 {
		core.Fail(w, 400, errors.New("设备参数不合法"))
		return
	}
	if v.ProxyPort == 17891 || v.ProxyPort == 18090 {
		core.Fail(w, 400, errors.New("不能把受管代理入口再次配置为出口，避免循环"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.device(r.PathValue("id"))
	if d == nil {
		core.Fail(w, 404, errors.New("设备不存在"))
		return
	}
	old := *d
	d.Name = v.Name
	d.ProxyPort = v.ProxyPort
	d.ProxyType = v.ProxyType
	d.SSHUser = v.SSHUser
	d.SSHFingerprint = v.SSHFingerprint
	d.SSHPort = v.SSHPort
	s.audit("device.update", d.Name)
	if e := s.persist(); e != nil {
		*d = old
		core.Fail(w, 500, e)
		return
	}
	core.JSON(w, map[string]bool{"ok": true})
}
func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request) {
	var p core.Policy
	if e := core.Decode(w, r, &p); e != nil {
		core.Fail(w, 400, e)
		return
	}
	p.Source = r.PathValue("id")
	p.Active = ""
	if p.OnFailure != "block" && p.OnFailure != "direct" || len(p.Exits) > 20 {
		core.Fail(w, 400, errors.New("策略参数不合法"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.device(p.Source) == nil {
		core.Fail(w, 404, errors.New("来源设备不存在"))
		return
	}
	seen := map[string]bool{}
	for _, id := range p.Exits {
		d := s.device(id)
		if d == nil || d.ProxyPort == 0 || seen[id] {
			core.Fail(w, 400, errors.New("出口不存在、未设置代理或重复"))
			return
		}
		seen[id] = true
	}
	old := append([]core.Policy{}, s.state.Policies...)
	found := false
	for i, x := range s.state.Policies {
		if x.Source == p.Source {
			s.state.Policies[i] = p
			found = true
		}
	}
	if !found {
		s.state.Policies = append(s.state.Policies, p)
	}
	s.audit("policy.update", p.Source)
	if e := s.persist(); e != nil {
		s.state.Policies = old
		core.Fail(w, 500, e)
		return
	}
	core.JSON(w, p)
}
func (s *Server) ProxyHandler() http.Handler {
	return core.ProxyHandler(func(r *http.Request) (core.DialFunc, error) {
		id, t, ok := core.ProxyCredentials(r)
		s.mu.Lock()
		d := s.device(id)
		valid := ok && d != nil && core.Equal(d.Token, t)
		s.mu.Unlock()
		if !valid {
			return nil, errors.New("unauthorized")
		}
		return func(ctx context.Context, address string) (net.Conn, error) { return s.dialPolicy(ctx, id, address) }, nil
	})
}
func (s *Server) RunLocalProxy(ctx context.Context) error {
	return core.LocalProxy(ctx, s.cfg.LocalProxyListen, func(ctx context.Context, address string) (net.Conn, error) {
		return s.dialPolicy(ctx, "cloud", address)
	})
}
func (s *Server) dialPolicy(ctx context.Context, id, address string) (net.Conn, error) {
	s.mu.Lock()
	p := core.Policy{OnFailure: "block"}
	for _, x := range s.state.Policies {
		if x.Source == id {
			p = x
		}
	}
	var exits []core.Device
	for _, x := range p.Exits {
		if d := s.device(x); d != nil {
			exits = append(exits, *d)
		}
	}
	s.mu.Unlock()
	for _, d := range exits {
		u := d.ProxyType + "://" + core.HostPort(d.IP, d.ProxyPort)
		attempt, cancel := context.WithTimeout(ctx, 6*time.Second)
		c, e := core.DialVia(attempt, u, address)
		cancel()
		if e == nil {
			s.setActive(id, d.ID)
			return c, nil
		}
	}
	if p.OnFailure == "direct" {
		c, e := core.DialDirect(ctx, address)
		if e == nil {
			s.setActive(id, "DIRECT")
		}
		return c, e
	}
	s.setActive(id, "")
	return nil, errors.New("all exits failed; direct access disabled")
}
func (s *Server) setActive(id, active string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Policies {
		if s.state.Policies[i].Source == id {
			s.state.Policies[i].Active = active
		}
	}
}
func (s *Server) Run(ctx context.Context) {
	s.reconcilePeers()
	s.restoreMappings()
	s.probe(ctx)
	t := time.NewTicker(10 * time.Second)
	iterations := 0
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.forwardMu.Lock()
			for _, stop := range s.forwards {
				stop()
			}
			s.forwardMu.Unlock()
			return
		case <-t.C:
			iterations++
			if iterations%6 == 0 {
				s.reconcilePeers()
			}
			s.probe(ctx)
		}
	}
}
func (s *Server) reconcilePeers() {
	s.mu.Lock()
	devices := append([]core.Device{}, s.state.Devices...)
	s.mu.Unlock()
	existing, e := runWG("show", s.cfg.Interface, "allowed-ips")
	if e != nil {
		return
	}
	for _, d := range devices {
		if validKey(d.PublicKey) && net.ParseIP(d.IP) != nil {
			present, conflict := false, false
			for _, line := range strings.Split(existing, "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 && strings.Contains(f[1], d.IP+"/32") {
					if f[0] == d.PublicKey {
						present = true
					} else {
						conflict = true
					}
				}
			}
			if !present && !conflict {
				runWG("set", s.cfg.Interface, "peer", d.PublicKey, "allowed-ips", d.IP+"/32")
			}
		}
	}
}
func (s *Server) probe(ctx context.Context) {
	hand, _ := runWG("show", s.cfg.Interface, "latest-handshakes")
	transfer, _ := runWG("show", s.cfg.Interface, "transfer")
	h := map[string]int64{}
	rx := map[string]int64{}
	tx := map[string]int64{}
	for _, l := range strings.Split(hand, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			h[f[0]], _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	for _, l := range strings.Split(transfer, "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			rx[f[0]], _ = strconv.ParseInt(f[1], 10, 64)
			tx[f[0]], _ = strconv.ParseInt(f[2], 10, 64)
		}
	}
	s.mu.Lock()
	devices := append([]core.Device{}, s.state.Devices...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, d := range devices {
		wg.Add(1)
		go func(d core.Device) {
			defer wg.Done()
			healthy := false
			msg := "未配置共享代理"
			if d.ProxyPort > 0 {
				tr := &http.Transport{DialContext: func(c context.Context, n, a string) (net.Conn, error) {
					return core.DialVia(c, d.ProxyType+"://"+core.HostPort(d.IP, d.ProxyPort), a)
				}}
				client := &http.Client{Transport: tr, Timeout: 6 * time.Second}
				req, e := http.NewRequestWithContext(ctx, "HEAD", s.cfg.HealthURL, nil)
				if e == nil {
					resp, e := client.Do(req)
					if e == nil {
						resp.Body.Close()
						healthy = resp.StatusCode < 500
						msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
					} else {
						msg = "出口请求失败"
					}
				}
				tr.CloseIdleConnections()
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			target := s.device(d.ID)
			if target != nil {
				target.Handshake = h[d.PublicKey]
				target.RX = rx[d.PublicKey]
				target.TX = tx[d.PublicKey]
				target.ProxyHealthy = healthy
				target.ProxyError = msg
			}
		}(d)
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist()
}
func (s *Server) Backup() error {
	b, e := os.ReadFile(s.path)
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(filepath.Dir(s.path), "backup-"+time.Now().Format("20060102-150405")+".json"), b, 0600)
}
