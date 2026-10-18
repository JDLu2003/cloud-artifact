package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	assets "vps-artifact"
	"vps-artifact/internal/artifact"
	"vps-artifact/internal/auth"
	"vps-artifact/internal/blob"
	"vps-artifact/internal/config"
	"vps-artifact/internal/metrics"
	"vps-artifact/internal/skillpack"
	"vps-artifact/internal/store"
)

type env struct {
	ts    *httptest.Server
	srv   *Server
	key   string
	admin *store.User
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bl, _ := blob.New(filepath.Join(dir, "blobs"))
	libFS, _ := fs.Sub(assets.FS, "web/lib")
	adminFS, _ := fs.Sub(assets.FS, "web/admin")
	viewerFS, _ := fs.Sub(assets.FS, "web/viewer")
	skillFS, _ := fs.Sub(assets.FS, "skill/artifact")
	pack, err := skillpack.New(skillFS)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dir, MaxBodyMB: 16, AdminUser: "admin"}
	signer := auth.NewSigner("test-secret")
	svc := &artifact.Service{Store: st, Blobs: bl, Cfg: cfg, Signer: signer, LibExists: LibExists(libFS)}
	srv := &Server{Cfg: cfg, Store: st, Svc: svc, Signer: signer, Limiter: auth.NewLimiter(), Metrics: metrics.New(),
		Skill: pack, Version: "test", LibFS: libFS, AdminFS: adminFS, ViewerFS: viewerFS}
	if err := srv.Init(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	cfg.PublicURL = ts.URL
	hash, _ := auth.HashPassword("correct-horse")
	admin, _ := st.CreateUser(store.NewUser{Name: "admin", PasswordHash: hash, Role: "admin"})
	key, _, _ := srv.IssueKey(admin.ID, "test", 0)
	return &env{ts: ts, srv: srv, key: key, admin: admin}
}

func (e *env) call(t *testing.T, method, path, key string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestAgentAPIAndAccess(t *testing.T) {
	e := setup(t)
	if r, _ := e.call(t, "GET", "/api/v1/me", "", nil); r.StatusCode != 401 {
		t.Fatalf("no key: %d", r.StatusCode)
	}
	if r, _ := e.call(t, "GET", "/api/v1/me", "art_wrongwrongwrongwrongwrongwrong", nil); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}
	r, out := e.call(t, "POST", "/api/v1/artifacts", e.key, map[string]any{
		"files": map[string]any{"index.html": "<title>测试</title><p>hello</p>", "img/a.png": map[string]string{"content": "iVBORw0KGgo=", "encoding": "base64"}},
	})
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %v", r.StatusCode, out)
	}
	id := out["id"].(string)

	// 持链接可看：匿名可以看当前版本，内容带沙箱 CSP
	resp, err := http.Get(e.ts.URL + "/c/" + id + "/v1/-/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("content: %v %d", err, resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.HasPrefix(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") {
		t.Fatalf("content must be sandboxed without same-origin: %s", csp)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<!doctype html>") {
		t.Fatal("page without skeleton should be wrapped")
	}

	// 发布 v2 后，v1 不带签名不可访问；带签名可以
	e.call(t, "PUT", "/api/v1/artifacts/"+id, e.key, map[string]any{"files": map[string]any{"index.html": "<title>测试</title><p>v2</p>"}})
	if resp, _ := http.Get(e.ts.URL + "/c/" + id + "/v1/-/index.html"); resp.StatusCode != 403 {
		t.Fatalf("old version without token should be 403, got %d", resp.StatusCode)
	}
	tok := e.srv.Signer.ContentToken(id, 1, contentTTL)
	if resp, _ := http.Get(e.ts.URL + "/c/" + id + "/v1/" + tok + "/index.html"); resp.StatusCode != 200 {
		t.Fatalf("signed old version should be 200, got %d", resp.StatusCode)
	}
	if resp, _ := http.Get(e.ts.URL + "/c/" + id + "/v1/" + tok + "x/index.html"); resp.StatusCode != 403 {
		t.Fatalf("tampered token should be 403, got %d", resp.StatusCode)
	}

	// 改为私有后，匿名访问查看页和内容都被拒绝
	e.call(t, "PATCH", "/api/v1/artifacts/"+id, e.key, map[string]any{"visibility": "private"})
	if resp, _ := http.Get(e.ts.URL + "/a/" + id); resp.StatusCode != 404 {
		t.Fatalf("private view page should be 404 for anonymous, got %d", resp.StatusCode)
	}
	if resp, _ := http.Get(e.ts.URL + "/c/" + id + "/v2/-/"); resp.StatusCode != 403 {
		t.Fatalf("private content should be 403, got %d", resp.StatusCode)
	}

	// 其他用户的 Key 看不到这个 artifact
	bob, _ := e.srv.Store.CreateUser(store.NewUser{Name: "bob"})
	bobKey, _, _ := e.srv.IssueKey(bob.ID, "b", 0)
	if r, _ := e.call(t, "GET", "/api/v1/artifacts/"+id, bobKey, nil); r.StatusCode != 404 {
		t.Fatalf("other user should get 404, got %d", r.StatusCode)
	}

	// 吊销后的 Key 不能用
	k, _ := e.srv.Store.KeyByHash(auth.HashToken(bobKey))
	e.srv.Store.RevokeKey(k.ID)
	if r, _ := e.call(t, "GET", "/api/v1/me", bobKey, nil); r.StatusCode != 401 {
		t.Fatalf("revoked key should be 401, got %d", r.StatusCode)
	}
}

func TestAdminSessionAndCSRF(t *testing.T) {
	e := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	post := func(path, csrf string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.ts.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if r := post("/api/admin/login", "", map[string]string{"username": "admin", "password": "wrong-pass"}); r.StatusCode != 401 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	r := post("/api/admin/login", "", map[string]string{"username": "admin", "password": "correct-horse"})
	if r.StatusCode != 200 {
		t.Fatalf("login: %d", r.StatusCode)
	}
	var lo struct{ CSRF string }
	json.NewDecoder(r.Body).Decode(&lo)
	if r := post("/api/admin/keys", "", map[string]any{"name": "x"}); r.StatusCode != 403 {
		t.Fatalf("write without CSRF should be 403, got %d", r.StatusCode)
	}
	r = post("/api/admin/keys", lo.CSRF, map[string]any{"name": "x"})
	if r.StatusCode != 201 {
		t.Fatalf("issue key: %d", r.StatusCode)
	}
	var kr struct{ Key, Prompt string }
	json.NewDecoder(r.Body).Decode(&kr)
	if !strings.HasPrefix(kr.Key, "art_") || !strings.Contains(kr.Prompt, kr.Key) || !strings.Contains(kr.Prompt, "/skill.tar.gz") {
		t.Fatalf("prompt should contain key and install url: %+v", kr)
	}
	resp, _ := c.Get(e.ts.URL + "/api/admin/overview")
	var ov map[string]any
	json.NewDecoder(resp.Body).Decode(&ov)
	if ov["system"] == nil || ov["counts"] == nil {
		t.Fatalf("admin overview should include system metrics: %v", ov)
	}
}

func TestSkillTarball(t *testing.T) {
	e := setup(t)
	resp, err := http.Get(e.ts.URL + "/skill.tar.gz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		found[h.Name] = string(b)
	}
	skill, ok := found["artifact/SKILL.md"]
	if !ok || !strings.HasPrefix(skill, "---\nname: artifact\n") {
		t.Fatal("SKILL.md missing or without frontmatter")
	}
	if strings.Contains(skill, "{{SERVER_URL}}") || !strings.Contains(skill, e.ts.URL) {
		t.Fatal("server url placeholder should be rendered")
	}
	if py := found["artifact/scripts/artifact.py"]; !strings.Contains(py, `SKILL_VERSION = "`+e.srv.Skill.Version+`"`) {
		t.Fatal("script should carry the skill version")
	}
}
