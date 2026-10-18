package artifact

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"vps-artifact/internal/auth"
	"vps-artifact/internal/blob"
	"vps-artifact/internal/config"
	"vps-artifact/internal/store"
)

func newTestService(t *testing.T) (*Service, Actor) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bl, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.NewUser{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PublicURL: "http://example.test", DataDir: dir, MinFreeDiskMB: 0}
	svc := &Service{Store: st, Blobs: bl, Cfg: cfg, Signer: auth.NewSigner("k")}
	return svc, Actor{User: u, IP: "127.0.0.1"}
}

func files(kv ...string) map[string]*FileInput {
	m := map[string]*FileInput{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = &FileInput{Content: []byte(kv[i+1])}
	}
	return m
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestPublishLifecycle(t *testing.T) {
	svc, a := newTestService(t)
	ctx := context.Background()

	r1, err := svc.Publish(ctx, PublishInput{Files: files("index.html", "<title>周报</title><p>hi</p>", "a.css", "p{}"), Actor: a})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.Created || r1.Version != 1 || r1.Title != "周报" || r1.Visibility != "unlisted" {
		t.Fatalf("unexpected create result: %+v", r1)
	}

	// merge：只传一个新文件，旧文件保留
	base := 1
	r2, err := svc.Publish(ctx, PublishInput{ArtifactID: r1.ID, BaseVersion: &base, Files: files("b.js", "1"), Actor: a})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Version != 2 || r2.Files != 3 {
		t.Fatalf("merge: want v2 with 3 files, got %+v", r2)
	}

	// 基于旧版本发布 → 冲突
	_, err = svc.Publish(ctx, PublishInput{ArtifactID: r1.ID, BaseVersion: &base, Files: files("c.txt", "x"), Actor: a})
	if code(err) != "conflict" {
		t.Fatalf("want conflict, got %v", err)
	}

	// replace：只剩传入的文件
	r3, err := svc.Publish(ctx, PublishInput{ArtifactID: r1.ID, Replace: true, Files: files("index.html", "<title>周报</title><p>v3</p>"), Actor: a})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Files != 1 {
		t.Fatalf("replace: want 1 file, got %d", r3.Files)
	}

	// 内容不变不生成新版本
	r4, err := svc.Publish(ctx, PublishInput{ArtifactID: r1.ID, Replace: true, Files: files("index.html", "<title>周报</title><p>v3</p>"), Actor: a})
	if err != nil || !r4.Unchanged || r4.Version != 3 {
		t.Fatalf("unchanged: %+v %v", r4, err)
	}

	// 局部编辑
	r5, err := svc.Edit(ctx, a, r1.ID, nil, []Edit{{OldStr: "<p>v3</p>", NewStr: "<p>v5</p>"}}, "改一行")
	if err != nil || r5.Version != 4 {
		t.Fatalf("edit: %+v %v", r5, err)
	}
	if _, err := svc.Edit(ctx, a, r1.ID, nil, []Edit{{OldStr: "不存在", NewStr: "x"}}, ""); code(err) != "edit_failed" {
		t.Fatalf("edit miss: %v", err)
	}
	if _, err := svc.Edit(ctx, a, r1.ID, nil, []Edit{{OldStr: "p>", NewStr: "x"}}, ""); code(err) != "edit_failed" {
		t.Fatalf("edit ambiguous: %v", err)
	}

	// 回滚到 v2：生成 v5，文件与 v2 相同
	r6, err := svc.Restore(ctx, a, r1.ID, 2)
	if err != nil || r6.Version != 5 || r6.Files != 3 {
		t.Fatalf("restore: %+v %v", r6, err)
	}
	d, err := svc.DiffVersions(r1.ID, 2, 5, true)
	if err != nil || d.Changed != 0 {
		t.Fatalf("v2 and v5 should be identical: %+v %v", d, err)
	}
	d, err = svc.DiffVersions(r1.ID, 3, 4, true)
	if err != nil || d.Changed != 1 || d.Added != 1 || d.Removed != 1 {
		t.Fatalf("diff v3→v4: %+v %v", d, err)
	}

	// 删除后不能再发布
	if err := svc.Delete(a, r1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, PublishInput{ArtifactID: r1.ID, Files: files("x.txt", "x"), Actor: a}); code(err) != "deleted" {
		t.Fatalf("want deleted, got %v", err)
	}
	if err := svc.Purge(a, r1.ID); err != nil {
		t.Fatal(err)
	}
	n, _, err := svc.GC()
	if err != nil || n == 0 {
		t.Fatalf("gc should remove blobs: n=%d err=%v", n, err)
	}
}

func TestPermissionsAndQuota(t *testing.T) {
	svc, a := newTestService(t)
	ctx := context.Background()
	r, err := svc.Publish(ctx, PublishInput{Files: files("index.html", "<title>x</title>"), Actor: a})
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := svc.Store.CreateUser(store.NewUser{Name: "bob"})
	b := Actor{User: bob}
	if _, err := svc.Publish(ctx, PublishInput{ArtifactID: r.ID, Files: files("index.html", "hack"), Actor: b}); code(err) != "not_found" {
		t.Fatalf("other user must not write: %v", err)
	}
	one := int64(1)
	svc.Store.UpdateUser(bob.ID, store.UserPatch{QuotaStorageMB: &one})
	bob, _ = svc.Store.UserByID(bob.ID)
	big := strings.Repeat("x", 1<<20+10)
	if _, err := svc.Publish(ctx, PublishInput{Files: files("index.html", big), Actor: Actor{User: bob}}); code(err) != "quota_storage" {
		t.Fatalf("want quota_storage, got %v", err)
	}
}

func TestCleanPath(t *testing.T) {
	ok := map[string]string{"a.html": "a.html", "./img/x.png": "img/x.png", `img\y.png`: "img/y.png", "中文/图.png": "中文/图.png"}
	for in, want := range ok {
		got, err := CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", ".env", "a/.git/config", "a#b", "a?b", "a//b"} {
		if _, err := CleanPath(bad); err == nil {
			t.Errorf("CleanPath(%q) should fail", bad)
		}
	}
}

func TestWrapHTML(t *testing.T) {
	out := string(WrapHTML([]byte(`<title>t</title><pre class="mermaid">graph LR; A-->B</pre>`)))
	if !strings.HasPrefix(out, "<!doctype html>") || !strings.Contains(out, "/_lib/mermaid/mermaid.min.js") {
		t.Fatalf("wrap failed: %s", out)
	}
	full := `<!doctype html><html><head><title>x</title></head><body>hi</body></html>`
	if string(WrapHTML([]byte(full))) != full {
		t.Fatal("full document should be untouched")
	}
}

func TestRenderMarkdown(t *testing.T) {
	doc, err := RenderMarkdown([]byte("---\ntitle: 说明\n---\n# 标题\n\n## 第一节\n\n```mermaid\ngraph LR\nA-->B\n```\n\n## 第二节\n\n## 第三节\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "说明" || len(doc.TOC) != 3 || !doc.HasMermaid {
		t.Fatalf("unexpected doc: %+v", doc)
	}
	if !strings.Contains(string(doc.Body), `id="第一节"`) {
		t.Fatalf("heading id should keep CJK: %s", doc.Body)
	}
}

func TestInlineDiff(t *testing.T) {
	a := ".shell{max-width:1160px;margin:auto}.top{display:flex;justify-content:space-between}"
	b := ".shell{max-width:908px;margin:auto}.top{display:flex;justify-content:flex-end}"
	la, lb, ok := inlineDiff(a, b)
	if !ok {
		t.Fatal("similar lines should get inline diff")
	}
	var ca, cb []string
	for _, s := range la {
		if s.Changed {
			ca = append(ca, s.Text)
		}
	}
	for _, s := range lb {
		if s.Changed {
			cb = append(cb, s.Text)
		}
	}
	if ca[0] != "1160px" || cb[0] != "908px" {
		t.Fatalf("first change should be the width: %q / %q", ca, cb)
	}
	if len(ca) < 2 || len(cb) < 2 {
		t.Fatalf("expected two separate changed segments, got %q / %q", ca, cb)
	}
}
