package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicSaveReplacesConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	for _, v := range []string{"before", "after"} {
		if e := Save(p, map[string]string{"value": v}); e != nil {
			t.Fatal(e)
		}
	}
	var v map[string]string
	if e := Load(p, &v); e != nil || v["value"] != "after" {
		t.Fatal("replace failed")
	}
	files, _ := os.ReadDir(filepath.Dir(p))
	if len(files) != 1 {
		t.Fatal("temporary file leaked")
	}
}
func TestProxyStripsCredentials(t *testing.T) {
	seen := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Proxy-Authorization")
		io.WriteString(w, "ok")
	}))
	defer target.Close()
	proxy := httptest.NewServer(ProxyHandler(func(r *http.Request) (DialFunc, error) { return DialDirect, nil }))
	defer proxy.Close()
	req := httptest.NewRequest("GET", target.URL, strings.NewReader(""))
	req.Header.Set("Proxy-Authorization", "Basic secret")
	w := httptest.NewRecorder()
	ProxyHandler(func(r *http.Request) (DialFunc, error) { return DialDirect, nil }).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if h := <-seen; h != "" {
		t.Fatal("proxy credential leaked to destination")
	}
}
