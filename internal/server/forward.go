package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"netconductor/internal/core"
	"strings"
	"sync"
	"time"
)

func (s *Server) validateMapping(m core.Mapping) error {
	if m.ListenPort < 1024 || !core.Port(m.ListenPort) || !core.Port(m.TargetPort) || len(m.Name) < 1 || len(m.Name) > 80 {
		return errors.New("端口范围或名称不合法")
	}
	if m.Protocol != "tcp" && m.Protocol != "udp" && m.Protocol != "http" {
		return errors.New("仅支持 TCP、UDP、HTTP")
	}
	if strings.ContainsAny(m.Domain, "/\\\r\n :") || len(m.Domain) > 253 {
		return errors.New("域名不合法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.device(m.Device) == nil {
		return errors.New("目标设备不存在")
	}
	if s.device(m.Device).IP == s.cfg.ServerIP && m.ListenPort == m.TargetPort {
		return errors.New("映射不能转回自己的监听端口")
	}
	for _, x := range s.state.Mappings {
		if x.ID != m.ID && x.ListenPort == m.ListenPort && ((x.Protocol == "udp") == (m.Protocol == "udp")) {
			return errors.New("监听端口已分配")
		}
	}
	return nil
}
func (s *Server) putMapping(w http.ResponseWriter, r *http.Request) {
	var m core.Mapping
	if e := core.Decode(w, r, &m); e != nil {
		core.Fail(w, 400, e)
		return
	}
	if m.ID != "" {
		core.Fail(w, 400, errors.New("请删除旧映射后新建"))
		return
	}
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	if e := s.validateMapping(m); e != nil {
		core.Fail(w, 400, e)
		return
	}
	m.ID = core.Token()[:12]
	m.Error = ""
	var stop func()
	var e error
	if m.Enabled {
		stop, e = s.startMapping(m)
		if e != nil {
			core.Fail(w, 409, e)
			return
		}
	}
	s.mu.Lock()
	s.state.Mappings = append(s.state.Mappings, m)
	s.audit("mapping.create", m.Name)
	e = s.persist()
	if e != nil {
		s.state.Mappings = s.state.Mappings[:len(s.state.Mappings)-1]
	}
	s.mu.Unlock()
	if e != nil {
		if stop != nil {
			stop()
		}
		core.Fail(w, 500, e)
		return
	}
	if stop != nil {
		s.forwards[m.ID] = stop
	}
	core.JSON(w, m)
}
func (s *Server) deleteMapping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	s.mu.Lock()
	old := append([]core.Mapping{}, s.state.Mappings...)
	found := false
	for i, m := range s.state.Mappings {
		if m.ID == id {
			s.state.Mappings = append(s.state.Mappings[:i], s.state.Mappings[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		core.Fail(w, 404, errors.New("映射不存在"))
		return
	}
	s.audit("mapping.delete", id)
	e := s.persist()
	if e != nil {
		s.state.Mappings = old
	}
	s.mu.Unlock()
	if e != nil {
		core.Fail(w, 500, e)
		return
	}
	if stop := s.forwards[id]; stop != nil {
		stop()
		delete(s.forwards, id)
	}
	core.JSON(w, map[string]bool{"ok": true})
}
func (s *Server) restoreMappings() {
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	s.mu.Lock()
	maps := append([]core.Mapping{}, s.state.Mappings...)
	s.mu.Unlock()
	for _, m := range maps {
		if !m.Enabled {
			continue
		}
		stop, e := s.startMapping(m)
		if e != nil {
			s.mu.Lock()
			for i := range s.state.Mappings {
				if s.state.Mappings[i].ID == m.ID {
					s.state.Mappings[i].Error = e.Error()
				}
			}
			s.mu.Unlock()
		} else {
			s.forwards[m.ID] = stop
		}
	}
}
func (s *Server) startMapping(m core.Mapping) (func(), error) {
	s.mu.Lock()
	d := s.device(m.Device)
	if d == nil {
		s.mu.Unlock()
		return nil, errors.New("target missing")
	}
	target := core.HostPort(d.IP, m.TargetPort)
	s.mu.Unlock()
	bind := core.HostPort(s.cfg.MapBind, m.ListenPort)
	if m.Protocol == "udp" {
		return startUDP(bind, target)
	}
	ln, e := net.Listen("tcp", bind)
	if e != nil {
		return nil, errors.New("端口监听失败：端口已占用或无权限")
	}
	if m.Protocol == "http" {
		u, _ := url.Parse("http://" + target)
		p := httputil.NewSingleHostReverseProxy(u)
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { http.Error(w, "backend unavailable", 502) }
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if x, _, e := net.SplitHostPort(host); e == nil {
				host = x
			}
			if m.Domain != "" && !strings.EqualFold(host, m.Domain) {
				http.NotFound(w, r)
				return
			}
			p.ServeHTTP(w, r)
		})
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		return func() { srv.Close() }, nil
	}
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	closed := false
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			if closed || len(active) >= 512 {
				mu.Unlock()
				c.Close()
				continue
			}
			active[c] = true
			mu.Unlock()
			go func() {
				defer func() { mu.Lock(); delete(active, c); mu.Unlock() }()
				b, e := net.DialTimeout("tcp", target, 5*time.Second)
				if e != nil {
					c.Close()
					return
				}
				mu.Lock()
				if closed {
					mu.Unlock()
					c.Close()
					b.Close()
					return
				}
				active[b] = true
				mu.Unlock()
				defer func() { mu.Lock(); delete(active, b); mu.Unlock() }()
				core.Bridge(c, b)
			}()
		}
	}()
	return func() {
		ln.Close()
		mu.Lock()
		closed = true
		for c := range active {
			c.Close()
		}
		mu.Unlock()
	}, nil
}
func startUDP(bind, target string) (func(), error) {
	addr, e := net.ResolveUDPAddr("udp", bind)
	if e != nil {
		return nil, e
	}
	ln, e := net.ListenUDP("udp", addr)
	if e != nil {
		return nil, e
	}
	dst, e := net.ResolveUDPAddr("udp", target)
	if e != nil {
		ln.Close()
		return nil, e
	}
	var mu sync.Mutex
	peers := map[string]*net.UDPConn{}
	closed := false
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, e := ln.ReadFromUDP(buf)
			if e != nil {
				return
			}
			key := from.String()
			mu.Lock()
			if closed {
				mu.Unlock()
				return
			}
			c := peers[key]
			if c == nil {
				if len(peers) >= 256 {
					mu.Unlock()
					continue
				}
				c, e = net.DialUDP("udp", nil, dst)
				if e != nil {
					mu.Unlock()
					continue
				}
				peers[key] = c
				go func(c *net.UDPConn, from *net.UDPAddr, key string) {
					defer c.Close()
					defer func() { mu.Lock(); delete(peers, key); mu.Unlock() }()
					b := make([]byte, 65535)
					for {
						c.SetReadDeadline(time.Now().Add(30 * time.Second))
						n, e := c.Read(b)
						if e != nil {
							return
						}
						if _, e = ln.WriteToUDP(b[:n], from); e != nil {
							return
						}
					}
				}(c, from, key)
			}
			_, _ = c.Write(buf[:n])
			mu.Unlock()
		}
	}()
	return func() {
		ln.Close()
		mu.Lock()
		closed = true
		for _, c := range peers {
			c.Close()
		}
		mu.Unlock()
	}, nil
}

var _ io.Reader
