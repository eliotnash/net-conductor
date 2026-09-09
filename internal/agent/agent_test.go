package agent

import (
	"io/fs"
	"net/http/httptest"
	"netconductor/internal/core"
	"netconductor/web"
	"path/filepath"
	"testing"
)

func TestLocalAPIRequiresTokenAndRejectsRebinding(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	core.Save(p, core.AgentConfig{LocalToken: "test-local-token", LocalListen: "127.0.0.1:18765", ProxyListen: "127.0.0.1:17891", Interface: "wg-test"})
	a, e := New(p)
	if e != nil {
		t.Fatal(e)
	}
	assets, _ := fs.Sub(web.Files, "static")
	for _, x := range []struct {
		host, token string
		status      int
	}{{"127.0.0.1:18765", "", 401}, {"evil.example", "test-local-token", 403}, {"127.0.0.1:18765", "test-local-token", 200}} {
		r := httptest.NewRequest("GET", "http://"+x.host+"/api/local/state", nil)
		r.Header.Set("Authorization", "Bearer "+x.token)
		w := httptest.NewRecorder()
		a.Handler(assets).ServeHTTP(w, r)
		if w.Code != x.status {
			t.Fatalf("%s got %d", x.host, w.Code)
		}
	}
}
func TestRejectMalformedCertificate(t *testing.T) {
	if _, e := makeClient("not a certificate"); e == nil {
		t.Fatal("malformed pin accepted")
	}
}
