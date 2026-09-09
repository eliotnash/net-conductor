package core

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DialFunc func(context.Context, string) (net.Conn, error)

func DialDirect(ctx context.Context, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
}
func DialVia(ctx context.Context, upstream, address string) (net.Conn, error) {
	u, e := url.Parse(upstream)
	if e != nil {
		return nil, e
	}
	if u.Scheme == "socks5" {
		var auth *proxy.Auth
		if u.User != nil {
			p, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: p}
		}
		d, e := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 8 * time.Second})
		if e != nil {
			return nil, e
		}
		return d.(proxy.ContextDialer).DialContext(ctx, "tcp", address)
	}
	if u.Scheme != "http" {
		return nil, errors.New("unsupported proxy scheme")
	}
	c, e := DialDirect(ctx, u.Host)
	if e != nil {
		return nil, e
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetDeadline(deadline)
	req := &http.Request{Method: "CONNECT", URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if u.User != nil {
		p, _ := u.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+p)))
	}
	if e = req.Write(c); e != nil {
		c.Close()
		return nil, e
	}
	br := bufio.NewReader(c)
	resp, e := http.ReadResponse(br, req)
	if e != nil {
		c.Close()
		return nil, e
	}
	if resp.StatusCode != 200 {
		c.Close()
		return nil, fmt.Errorf("upstream CONNECT returned %d", resp.StatusCode)
	}
	c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: br}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
func (c *bufferedConn) CloseWrite() error {
	if x, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return x.CloseWrite()
	}
	return nil
}
func Bridge(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 1)
	go func() {
		io.Copy(a, b)
		if x, ok := a.(interface{ CloseWrite() error }); ok {
			x.CloseWrite()
		}
		done <- struct{}{}
	}()
	io.Copy(b, a)
	if x, ok := b.(interface{ CloseWrite() error }); ok {
		x.CloseWrite()
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
}
func ProxyHandler(authorize func(*http.Request) (DialFunc, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dial, e := authorize(r)
		if e != nil {
			w.Header().Set("Proxy-Authenticate", "Basic realm=NetConductor")
			http.Error(w, "proxy authentication required", 407)
			return
		}
		if r.Method == "CONNECT" {
			if _, _, e = net.SplitHostPort(r.Host); e != nil {
				http.Error(w, "invalid destination", 400)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			dest, e := dial(ctx, r.Host)
			if e != nil {
				http.Error(w, "all configured exits unavailable", 502)
				return
			}
			client, rw, e := w.(http.Hijacker).Hijack()
			if e != nil {
				dest.Close()
				return
			}
			rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			rw.Flush()
			Bridge(&bufferedConn{client, rw.Reader}, dest)
			return
		}
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			http.Error(w, "HTTP proxy requires absolute URL", 400)
			return
		}
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.Header = r.Header.Clone()
		stripHop(out.Header)
		tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) { return dial(ctx, address) }, DisableKeepAlives: true, ResponseHeaderTimeout: 20 * time.Second}
		defer tr.CloseIdleConnections()
		resp, e := tr.RoundTrip(out)
		if e != nil {
			http.Error(w, "all configured exits unavailable", 502)
			return
		}
		defer resp.Body.Close()
		stripHop(resp.Header)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}
func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, k := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(k))
		}
	}
	for _, k := range []string{"Proxy-Authorization", "Proxy-Authenticate", "Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}
func ProxyCredentials(r *http.Request) (string, string, bool) {
	h := strings.TrimPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
	b, e := base64.StdEncoding.DecodeString(h)
	if e != nil {
		return "", "", false
	}
	u, p, ok := strings.Cut(string(b), ":")
	return u, p, ok
}
