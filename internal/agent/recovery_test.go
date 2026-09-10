package agent

import (
	"net"
	"netconductor/internal/core"
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
