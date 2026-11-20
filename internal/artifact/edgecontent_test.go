package artifact

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JDLu2003/vps-cluster/s3/s3test"

	"vps-artifact/internal/auth"
	"vps-artifact/internal/blob"
	"vps-artifact/internal/config"
	"vps-artifact/internal/store"
)

func newR2Service(t *testing.T) (*Service, Actor, *s3test.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake, c := s3test.New()
	t.Cleanup(fake.Close)
	bl, err := blob.NewS3(blob.S3Config{Endpoint: c.Endpoint, Bucket: c.Bucket, AccessKey: c.AccessKey, SecretKey: c.SecretKey, Region: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	bl.SetLocator(st.BlobLocation)
	u, err := st.CreateUser(store.NewUser{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PublicURL: "https://art.ex.com", DataDir: dir}
	svc := &Service{Store: st, Blobs: bl, Cfg: cfg, Signer: auth.NewSigner("k"), SharedBlobs: true,
		RenderMarkdown: func(art *store.Artifact, src []byte) ([]byte, error) {
			return []byte("<h1>" + art.Title + "</h1>" + string(src)), nil
		}}
	return svc, Actor{User: u, IP: "127.0.0.1"}, fake
}

func readJSON[T any](t *testing.T, f *s3test.Fake, key string) (T, bool) {
	t.Helper()
	var v T
	b, ok := f.Objs[key]
	if !ok {
		return v, false
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return v, true
}

func TestEdgeContentLifecycle(t *testing.T) {
	svc, a, f := newR2Service(t)
	vis := "public"
	title := "标题一"
	r, err := svc.Publish(context.Background(), PublishInput{Actor: a, Title: &title, Visibility: &vis,
		Files: files("index.html", "<p>hi</p>", "doc.md", "# x", "app.js", "1")})
	if err != nil {
		t.Fatal(err)
	}
	id := r.ID
	meta, ok := readJSON[EdgeMeta](t, f, "_m/"+id+".json")
	if !ok || meta.Visibility != "public" || meta.Current != 1 || meta.Deleted {
		t.Fatalf("meta = %+v %v (keys %v)", meta, ok, f.Keys())
	}
	man, ok := readJSON[EdgeManifest](t, f, "_m/"+id+"/v1.json")
	if !ok || man.Entry != "index.html" || len(man.Files) != 3 || !man.Files["index.html"].Rendered || man.Files["app.js"].Rendered {
		t.Fatalf("manifest = %+v", man)
	}
	if !strings.Contains(string(f.Objs["_r/"+id+"/v1/index.html"]), "<p>hi</p>") || string(f.Objs["_r/"+id+"/v1/index.html"]) == "<p>hi</p>" {
		t.Fatalf("HTML 应被包装: %q", f.Objs["_r/"+id+"/v1/index.html"])
	}
	if got := string(f.Objs["_r/"+id+"/v1/doc.md"]); got != "<h1>标题一</h1># x" {
		t.Fatalf("markdown render = %q", got)
	}

	// 第二版
	if _, err := svc.Publish(context.Background(), PublishInput{Actor: a, ArtifactID: id, Files: files("index.html", "<p>v2</p>")}); err != nil {
		t.Fatal(err)
	}
	if meta, _ = readJSON[EdgeMeta](t, f, "_m/"+id+".json"); meta.Current != 2 {
		t.Fatalf("current = %d", meta.Current)
	}

	// 改为私有：访问控制信息立即更新
	priv := "private"
	if _, err := svc.UpdateMeta(a, id, MetaPatch{Visibility: &priv}); err != nil {
		t.Fatal(err)
	}
	if meta, _ = readJSON[EdgeMeta](t, f, "_m/"+id+".json"); meta.Visibility != "private" {
		t.Fatalf("visibility = %s", meta.Visibility)
	}
	if err := svc.Delete(a, id); err != nil {
		t.Fatal(err)
	}
	if meta, _ = readJSON[EdgeMeta](t, f, "_m/"+id+".json"); !meta.Deleted {
		t.Fatal("删除后应标记 deleted")
	}
	if err := svc.Purge(a, id); err != nil {
		t.Fatal(err)
	}
	for _, k := range f.Keys() {
		if strings.HasPrefix(k, "_m/"+id) || strings.HasPrefix(k, "_r/"+id) {
			t.Fatalf("彻底删除后仍有分发对象 %s", k)
		}
	}

	// 全量补齐：删掉分发对象后可以重建
	r2, err := svc.Publish(context.Background(), PublishInput{Actor: a, Files: files("a.md", "# y")})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range f.Keys() {
		if strings.HasPrefix(k, "_") {
			delete(f.Objs, k)
		}
	}
	n, err := svc.EdgeResync(context.Background(), true)
	if err != nil || n != 1 {
		t.Fatalf("resync: %d %v", n, err)
	}
	for _, k := range []string{"_m/" + r2.ID + ".json", "_m/" + r2.ID + "/v1.json", "_r/" + r2.ID + "/v1/a.md", "_m/_settings.json"} {
		if _, ok := f.Objs[k]; !ok {
			t.Errorf("缺少 %s", k)
		}
	}
	set, _ := readJSON[EdgeSettings](t, f, "_m/_settings.json")
	if set.PublicURL != "https://art.ex.com" || len(set.CDNAllowlist) == 0 {
		t.Fatalf("settings = %+v", set)
	}
}
