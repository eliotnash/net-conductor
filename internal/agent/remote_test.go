package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"netconductor/internal/core"
	"netconductor/internal/remote"
	"strings"
	"testing"
	"time"
)

func TestLocalSSHAddress(t *testing.T) {
	for _, tc := range []struct {
		address string
		ok      bool
	}{{"", true}, {"127.0.0.1:22", true}, {"10.77.0.2:2222", true}, {"[::1]:22", true}, {"10.77.0.3:22", false}, {"example.org:22", false}, {"127.0.0.1:0", false}} {
		_, err := localSSHAddress(core.AgentConfig{IP: "10.77.0.2", SSHAddress: tc.address})
		if (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.address, err)
		}
	}
}
func TestDesktopJobHandoffAndLateReply(t *testing.T) {
	a := &Agent{}
	a.desktop.queue = make(chan desktopJob, 1)
	a.desktop.pending = map[string]chan desktopResult{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		v, e := a.desktopRequest(ctx, remote.Request{Op: "frame"})
		if e == nil && string(v.(json.RawMessage)) != `{"image":"test"}` {
			t.Error("reply corrupted")
		}
		result <- e
	}()
	w := httptest.NewRecorder()
	a.desktopNext(w, httptest.NewRequest("POST", "/", nil).WithContext(ctx))
	var job desktopJob
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" || job.Request.Op != "frame" {
		t.Fatal("job not delivered")
	}
	reply := `{"id":"` + job.ID + `","result":{"image":"test"}}`
	a.desktopReply(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reply)))
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	a.desktopReply(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(reply)))
	if len(a.desktop.pending) != 0 {
		t.Fatal("pending job leaked")
	}
	if a.desktopSeen.IsZero() {
		t.Fatal("tray readiness not recorded")
	}
}
