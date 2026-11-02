package blob

import (
	"os"
	"testing"
)

// 连接真实对象存储的集成测试。设置 TEST_S3_ENDPOINT / TEST_S3_BUCKET / TEST_S3_ACCESS_KEY / TEST_S3_SECRET_KEY 后才运行。
func TestS3Live(t *testing.T) {
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("未设置 TEST_S3_ENDPOINT")
	}
	s, err := NewS3(S3Config{Endpoint: ep, Bucket: os.Getenv("TEST_S3_BUCKET"), AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Prefix: "livetest/"})
	if err != nil {
		t.Fatal(err)
	}
	loc := Loc{ArtifactID: "LiveTest01", Version: 1, Path: "目录/文件 a+b(1)&c=d.html"}
	if err := s.PutAt(loc, []byte("live")); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteKey(loc.Key())
	if b, err := s.ReadKey(loc.Key()); err != nil || string(b) != "live" {
		t.Fatalf("读取 = %q, %v", b, err)
	}
	keys, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range keys {
		if k == loc.Key() {
			found = true
		}
	}
	if !found {
		t.Fatalf("List 中没有 %q: %v", loc.Key(), keys)
	}
}
