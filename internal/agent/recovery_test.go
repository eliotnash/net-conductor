package agent

import (
	"net"
	"netconductor/internal/core"
	"strings"
	"testing"
	"time"
)

func TestRecoveryBackoffCapsAndResets(t *testing.T) {
	var b recoveryBackoff
	now := time.Now()
	for _, want := range []time.Duration{30, 60, 120, 240, 300, 300} {
		b.record(now, true)
		if b.next.Sub(now) != want*time.Second {
			t.Fatal("incorrect retry delay", b.next.Sub(now))
		}
	}
	b.record(now, false)
	b.record(now, true)
	if b.delay != 30*time.Second {
		t.Fatal("backoff did not reset")
	}
}

func TestSharedListenerRecoversAfterAcceptFailure(t *testing.T) {
	a := &Agent{cfg: core.AgentConfig{IP: "127.0.0.1", HubProxy: "127.0.0.1:18090", Shares: []core.SharedPort{{ListenPort: 0, LocalPort: 1}}}, shares: map[int]net.Listener{}}
	a.restoreShares()
	old := a.shares[0]
	if old == nil {
		t.Fatal("listener missing")
	}
	old.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.op.Lock()
		cleared := a.shares[0] == nil
		a.op.Unlock()
		if cleared {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead listener still cached")
		}
		time.Sleep(time.Millisecond)
	}
	a.restoreShares()
	a.op.Lock()
	fresh := a.shares[0]
	a.op.Unlock()
	if fresh == nil || fresh == old {
		t.Fatal("listener not restored")
	}
	defer fresh.Close()
	conn, err := net.DialTimeout("tcp", fresh.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestSharedProxyRecoversWhenTargetStarts(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := target.Addr().(*net.TCPAddr).Port
	target.Close()
	a := &Agent{shares: map[int]net.Listener{}}
	ln, err := a.startShare(core.AgentConfig{IP: "127.0.0.1", HubProxy: "127.0.0.1:18090"}, core.SharedPort{ListenPort: 0, LocalPort: port})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	var b [1]byte
	if _, err = c.Read(b[:]); err == nil {
		t.Fatal("unavailable target falsely succeeded")
	}
	c.Close()
	target, err = net.Listen("tcp", core.HostPort("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, e := target.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		conn.Read(buf)
		conn.Write([]byte("HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nok"))
	}()
	c, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("GET / HTTP/1.0\r\n\r\n"))
	buf := make([]byte, 128)
	n, err := c.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "200 OK") {
		t.Fatalf("target restart did not recover: %v", err)
	}
}
