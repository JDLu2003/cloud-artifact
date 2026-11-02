package blob

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestS3RoundTrip(t *testing.T) {
	var mu sync.Mutex
	objs := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			w.Write([]byte("<ListBucketResult></ListBucketResult>"))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("x-amz-date") == "" {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			if Sum(b) != r.Header.Get("x-amz-content-sha256") {
				w.WriteHeader(400)
				return
			}
			objs[r.URL.EscapedPath()] = b
		case "GET", "HEAD":
			b, ok := objs[r.URL.EscapedPath()]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(b)
		case "DELETE":
			delete(objs, r.URL.EscapedPath())
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	s, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bk", AccessKey: "AK", SecretKey: "SK", Prefix: "p/"})
	if err != nil {
		t.Fatal(err)
	}
	loc := Loc{ArtifactID: "abc123XYZ0", Version: 2, Path: "dir/中文 a+b(1).html"}
	if err := s.PutAt(loc, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("对象数量 = %d", len(objs))
	}
	if !s.HasKey(loc.Key()) {
		t.Fatal("HasKey 应为 true")
	}
	if b, err := s.ReadKey(loc.Key()); err != nil || string(b) != "hello" {
		t.Fatalf("ReadKey = %q, %v", b, err)
	}
	s.SetLocator(func(sha string) (Loc, bool) { return loc, sha == Sum([]byte("hello")) })
	if b, err := s.Read(Sum([]byte("hello"))); err != nil || string(b) != "hello" {
		t.Fatalf("Read = %q, %v", b, err)
	}
	if err := s.DeleteKey(loc.Key()); err != nil || s.HasKey(loc.Key()) {
		t.Fatal("删除失败")
	}
	if _, err := s.ReadKey(loc.Key()); err == nil {
		t.Fatal("读取不存在的对象应报错")
	}
}

func TestKeyLayout(t *testing.T) {
	if k := Key("abc", 3, "a/b.html"); k != "abc/v3/a/b.html" {
		t.Fatal(k)
	}
	if !IsArtifactKey("abc/v3/a/b.html") || IsArtifactKey("ab/cdef") || !IsLegacyKey("00/"+strings.Repeat("a", 62)) {
		t.Fatal("键判断错误")
	}
}
