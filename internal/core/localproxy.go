package core

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// LocalProxy is a loopback-only HTTP/SOCKS5 TCP entry for trusted local programs.
// Both protocols use the same policy dialer; neither silently falls back to direct.
func LocalProxy(ctx context.Context, address string, dial DialFunc) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("local proxy must bind a literal loopback address")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return serveLocalProxy(ctx, listener, dial)
}

type httpConnections struct {
	net.Listener
	connections chan net.Conn
	done        chan struct{}
}

func (l *httpConnections) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func serveLocalProxy(ctx context.Context, listener net.Listener, dial DialFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer listener.Close()
	h := &httpConnections{Listener: listener, connections: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{Handler: ProxyHandler(func(*http.Request) (DialFunc, error) { return dial, nil }), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() { <-ctx.Done(); listener.Close(); close(h.done); srv.Close() }()
	go func() { _ = srv.Serve(h); cancel() }()
	for {
		c, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			r := bufio.NewReader(c)
			first, err := r.Peek(1)
			if err != nil {
				c.Close()
				return
			}
			wrapped := &bufferedConn{Conn: c, r: r}
			if first[0] == 5 {
				serveSOCKS(ctx, wrapped, dial)
				return
			}
			c.SetReadDeadline(time.Time{})
			select {
			case h.connections <- wrapped:
			case <-ctx.Done():
				c.Close()
			}
		}()
	}
}

func serveSOCKS(ctx context.Context, c net.Conn, dial DialFunc) {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(c, greeting[:]); err != nil || greeting[0] != 5 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	allowed := false
	for _, m := range methods {
		if m == 0 {
			allowed = true
		}
	}
	if !allowed {
		c.Write([]byte{5, 255})
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(c, request[:]); err != nil || request[0] != 5 || request[2] != 0 {
		return
	}
	reply := func(code byte) { c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if request[1] != 1 {
		reply(7)
		return
	} // CONNECT only: no BIND or UDP association.
	var host string
	switch request[3] {
	case 1, 4:
		size := 4
		if request[3] == 4 {
			size = 16
		}
		ip := make([]byte, size)
		if _, err := io.ReadFull(c, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(c, size[:]); err != nil || size[0] == 0 {
			return
		}
		name := make([]byte, int(size[0]))
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
	default:
		reply(8)
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return
	}
	if binary.BigEndian.Uint16(port[:]) == 0 {
		reply(1)
		return
	}
	c.SetDeadline(time.Now().Add(25 * time.Second))
	attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
	upstream, err := dial(attempt, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
	cancel()
	if err != nil {
		reply(1)
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUpstream()
	reply(0)
	c.SetDeadline(time.Time{})
	Bridge(c, upstream)
}
