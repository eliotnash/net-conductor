package core

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const Version = "0.2.0"

type Device struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	IP             string    `json:"ip"`
	PublicKey      string    `json:"publicKey"`
	Token          string    `json:"token,omitempty"`
	OS             string    `json:"os"`
	LastSeen       time.Time `json:"lastSeen"`
	Status         string    `json:"status"`
	LatencyMS      int64     `json:"latencyMs"`
	RX             int64     `json:"rx"`
	TX             int64     `json:"tx"`
	Handshake      int64     `json:"handshake"`
	ProxyPort      int       `json:"proxyPort"`
	ProxyType      string    `json:"proxyType"`
	ProxyHealthy   bool      `json:"proxyHealthy"`
	ProxyError     string    `json:"proxyError"`
	SSHUser        string    `json:"sshUser"`
	SSHFingerprint string    `json:"sshFingerprint"`
	SSHPort        int       `json:"sshPort"`
	AgentVersion   string    `json:"agentVersion"`
	Diagnostics    []Check   `json:"diagnostics"`
}
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}
type Policy struct {
	Source    string   `json:"source"`
	Exits     []string `json:"exits"`
	OnFailure string   `json:"onFailure"`
	Active    string   `json:"active"`
}
type Mapping struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	ListenPort int    `json:"listenPort"`
	Device     string `json:"device"`
	TargetPort int    `json:"targetPort"`
	Domain     string `json:"domain"`
	Enabled    bool   `json:"enabled"`
	Error      string `json:"error"`
}
type Event struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
}
type State struct {
	Devices  []Device  `json:"devices"`
	Policies []Policy  `json:"policies"`
	Mappings []Mapping `json:"mappings"`
	Events   []Event   `json:"events"`
}
type ServerConfig struct {
	LocalProxyListen string `json:"localProxyListen,omitempty"`
	Listen           string `json:"listen"`
	ProxyListen      string `json:"proxyListen"`
	PublicEndpoint   string `json:"publicEndpoint"`
	Interface        string `json:"interface"`
	ServerIP         string `json:"serverIP"`
	Network          string `json:"network"`
	AdminToken       string `json:"adminToken"`
	TLSCert          string `json:"tlsCert"`
	TLSKey           string `json:"tlsKey"`
	SSHKey           string `json:"sshKey"`
	HealthURL        string `json:"healthURL"`
	MapBind          string `json:"mapBind"`
}
type AgentConfig struct {
	SSHUser            string       `json:"sshUser,omitempty"`
	SSHKey             string       `json:"sshKey,omitempty"`
	SSHAuthorizedKeys  string       `json:"sshAuthorizedKeys,omitempty"`
	SSHHostFingerprint string       `json:"sshHostFingerprint,omitempty"`
	Shares             []SharedPort `json:"shares,omitempty"`
	Server             string       `json:"server"`
	Certificate        string       `json:"certificate"`
	ID                 string       `json:"id"`
	Token              string       `json:"token"`
	Name               string       `json:"name"`
	IP                 string       `json:"ip"`
	Interface          string       `json:"interface"`
	LocalListen        string       `json:"localListen"`
	ProxyListen        string       `json:"proxyListen"`
	HubProxy           string       `json:"hubProxy"`
	LocalToken         string       `json:"localToken"`
	WireGuardPath      string       `json:"wireGuardPath"`
	PrivateKey         string       `json:"privateKey,omitempty"`
	TunnelConfig       string       `json:"tunnelConfig,omitempty"`
}
type SharedPort struct {
	LocalPort  int `json:"localPort"`
	ListenPort int `json:"listenPort"`
}

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Equal(a, b string) bool {
	x := sha256.Sum256([]byte(a))
	y := sha256.Sum256([]byte(b))
	return a != "" && subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func Load(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func Save(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-*")
	if e != nil {
		return e
	}
	n := f.Name()
	defer os.Remove(n)
	if e = f.Chmod(0600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(n, path)
}
func JSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func Fail(w http.ResponseWriter, code int, e error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
}
func SameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, e := url.Parse(o)
	return e == nil && u.Host == r.Host
}
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if !SameOrigin(r) {
			Fail(w, 403, errors.New("cross-origin request denied"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func Port(p int) bool { return p > 0 && p <= 65535 }
func SafeName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func TCPCheck(address string) Check {
	start := time.Now()
	c, e := net.DialTimeout("tcp", address, 2*time.Second)
	if e != nil {
		return Check{address, false, "连接失败"}
	}
	c.Close()
	return Check{address, true, time.Since(start).Round(time.Millisecond).String()}
}
func HostPort(ip string, port int) string { return net.JoinHostPort(ip, fmtInt(port)) }
func fmtInt(n int) string                 { b, _ := json.Marshal(n); return string(b) }
func Bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
