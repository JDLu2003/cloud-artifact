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
			objs[r.URL.Path] = b
		case "GET", "HEAD":
			b, ok := objs[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(b)
		case "DELETE":
			delete(objs, r.URL.Path)
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	s, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bk", AccessKey: "AK", SecretKey: "SK", Prefix: "p/"})
	if err != nil {
		t.Fatal(err)
	}
	sha, err := s.Put([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := objs["/bk/p/"+sha[:2]+"/"+sha[2:]]; !ok {
		t.Fatalf("对象键不符合预期: %v", objs)
	}
	if !s.Has(sha) {
		t.Fatal("Has 应为 true")
	}
	if b, err := s.Read(sha); err != nil || string(b) != "hello" {
		t.Fatalf("Read = %q, %v", b, err)
	}
	if err := s.Delete(sha); err != nil || s.Has(sha) {
		t.Fatal("删除失败")
	}
	if _, err := s.Read(sha); err == nil {
		t.Fatal("读取不存在的对象应报错")
	}
}
