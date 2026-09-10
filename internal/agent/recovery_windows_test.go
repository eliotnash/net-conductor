package agent

import (
	"errors"
	"golang.org/x/sys/windows/svc"
	"testing"
)

type fakeTunnel struct {
	state  svc.State
	starts int
	err    error
}

func (f *fakeTunnel) Query() (svc.Status, error) { return svc.Status{State: f.state}, f.err }
func (f *fakeTunnel) Start(...string) error      { f.starts++; return nil }
func TestRecoveryStartsOnlyStoppedTunnel(t *testing.T) {
	for _, state := range []svc.State{svc.Running, svc.StartPending, svc.StopPending, svc.Stopped} {
		f := &fakeTunnel{state: state}
		action, err := startStoppedTunnel(f)
		if err != nil {
			t.Fatal(err)
		}
		expected := 0
		if state == svc.Stopped {
			expected = 1
		}
		if f.starts != expected || (action != "") != (expected == 1) {
			t.Fatalf("state %d unexpected start", state)
		}
	}
	f := &fakeTunnel{err: errors.New("query failed")}
	if _, err := startStoppedTunnel(f); err == nil || f.starts != 0 {
		t.Fatal("query error triggered recovery")
	}
}
