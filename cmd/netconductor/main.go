package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"netconductor/internal/agent"
	"netconductor/internal/core"
	"netconductor/internal/server"
	"netconductor/web"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func main() {
	mode := flag.String("mode", "server", "server, agent, init-server, init-agent")
	config := flag.String("config", "runtime/config.json", "configuration file")
	flag.Parse()
	if *mode == "init-server" {
		initServer(*config)
		return
	}
	if *mode == "init-agent" {
		initAgent(*config)
		return
	}
	run := func(ctx context.Context) {
		if e := serve(ctx, *mode, *config); e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	}
	if *mode == "agent" && runService(run) {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	run(ctx)
}
func serve(ctx context.Context, mode, path string) error {
	assets, _ := fs.Sub(web.Files, "static")
	if mode == "agent" {
		a, e := agent.New(path)
		if e != nil {
			return e
		}
		c := a.Config()
		go a.Run(ctx)
		proxy := &http.Server{Addr: c.ProxyListen, Handler: a.ProxyHandler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if e := proxy.ListenAndServe(); e != http.ErrServerClosed {
				log.Printf("proxy: %v", e)
			}
		}()
		srv := &http.Server{Addr: c.LocalListen, Handler: a.Handler(assets), ReadHeaderTimeout: 10 * time.Second}
		go func() { <-ctx.Done(); srv.Close(); proxy.Close() }()
		log.Printf("agent listening on %s", c.LocalListen)
		return srv.ListenAndServe()
	}
	var c core.ServerConfig
	if e := core.Load(path, &c); e != nil {
		return e
	}
	s, e := server.New(c, filepath.Join(filepath.Dir(path), "state.json"))
	if e != nil {
		return e
	}
	go s.Run(ctx)
	proxy := &http.Server{Addr: c.ProxyListen, Handler: s.ProxyHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if e := proxy.ListenAndServe(); e != http.ErrServerClosed {
			log.Printf("proxy: %v", e)
		}
	}()
	srv := &http.Server{Addr: c.Listen, Handler: s.Handler(assets), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { <-ctx.Done(); srv.Close(); proxy.Close() }()
	log.Printf("server listening on %s", c.Listen)
	return srv.ListenAndServeTLS(c.TLSCert, c.TLSKey)
}
func initServer(path string) {
	if _, e := os.Stat(path); e == nil {
		log.Fatal("configuration already exists")
	}
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0700)
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		log.Fatal(e)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Net Conductor"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.77.0.1")}}
	if ip := net.ParseIP(os.Getenv("NC_PUBLIC_IP")); ip != nil {
		t.IPAddresses = append(t.IPAddresses, ip)
	}
	der, e := x509.CreateCertificate(rand.Reader, t, t, &key.PublicKey, key)
	if e != nil {
		log.Fatal(e)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		log.Fatal(e)
	}
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0600); e != nil {
		log.Fatal(e)
	}
	c := core.ServerConfig{Listen: "0.0.0.0:18443", ProxyListen: "10.77.0.1:18090", PublicEndpoint: os.Getenv("NC_PUBLIC_IP") + ":51820", Interface: "wg0", ServerIP: "10.77.0.1", Network: "10.77.0.0/24", AdminToken: core.Token(), TLSCert: certPath, TLSKey: keyPath, SSHKey: filepath.Join(dir, "ssh_ed25519"), HealthURL: "https://www.gstatic.com/generate_204", MapBind: "0.0.0.0"}
	if e = core.Save(path, c); e != nil {
		log.Fatal(e)
	}
	fmt.Println("Initialized server config; admin password stored in protected config file.")
}
func initAgent(path string) {
	if _, e := os.Stat(path); e == nil {
		log.Fatal("configuration already exists")
	}
	host, _ := os.Hostname()
	c := core.AgentConfig{Name: host, LocalToken: core.Token(), LocalListen: "127.0.0.1:18765", ProxyListen: "127.0.0.1:17891", Interface: "nc-wg", WireGuardPath: `C:\Program Files\WireGuard`}
	if e := core.Save(path, c); e != nil {
		log.Fatal(e)
	}
	fmt.Println("Agent configuration initialized.")
}
