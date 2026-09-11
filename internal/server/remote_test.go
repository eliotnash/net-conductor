package server

import (
	"io/fs"
	"net/http/httptest"
	"netconductor/internal/core"
	"netconductor/web"
	"path/filepath"
	"testing"
)

func TestRemoteRoutesRequireAuthentication(t *testing.T) {
	s, err := New(core.ServerConfig{Interface: "wg-test", ServerIP: "10.77.0.1", AdminToken: core.Token()}, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, _ := fs.Sub(web.Files, "static")
	for _, path := range []string{"/api/remote/cloud?mode=files", "/api/remote/agent?id=cloud", "/api/agent/ssh-key?id=cloud"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		s.Handler(assets).ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("%s returned %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/remote/cloud?mode=desktop", nil)
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminToken)
	w := httptest.NewRecorder()
	s.Handler(assets).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("Linux desktop was not rejected")
	}
}
