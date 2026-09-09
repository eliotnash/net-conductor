package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestLocalProxyProtocols(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("through-policy")) }))
	defer target.Close()
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- serveLocalProxy(ctx, ln, func(ctx context.Context, address string) (net.Conn, error) {
					if address != target.Listener.Addr().String() {
						return nil, errors.New("unexpected target")
					}
					return DialDirect(ctx, address)
				})
			}()
			upstream := scheme + "://" + ln.Addr().String()
			tr := &http.Transport{}
			if scheme == "http" {
				u, _ := url.Parse(upstream)
				tr.Proxy = http.ProxyURL(u)
			} else {
				tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					return DialVia(ctx, upstream, address)
				}
			}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			resp, err := client.Get(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "through-policy" {
				t.Fatal(string(body))
			}
			c, err := DialVia(ctx, upstream, target.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown timeout")
			}
		})
	}
}

func TestLocalProxyRejectsPublicBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "10.77.0.1:0", ":0", "localhost:0"} {
		if err := LocalProxy(context.Background(), address, DialDirect); err == nil {
			t.Fatal("accepted", address)
		}
	}
}

func TestLocalProxyNoDirectFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go serveLocalProxy(ctx, ln, func(context.Context, string) (net.Conn, error) { return nil, errors.New("policy blocked") })
	for _, scheme := range []string{"http", "socks5"} {
		c, err := DialVia(ctx, scheme+"://"+ln.Addr().String(), "example.com:443")
		if err == nil {
			c.Close()
			t.Fatal("policy bypass", scheme)
		}
	}
}
