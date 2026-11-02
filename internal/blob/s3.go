package blob

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Config 描述一个 S3 兼容的对象存储（Cloudflare R2、MinIO、AWS S3 等）。
type S3Config struct {
	Endpoint  string // 如 https://<account>.r2.cloudflarestorage.com
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // R2 用 auto
	Prefix    string // 可选：对象键前缀，如 blobs/
}

type s3Backend struct {
	cfg    S3Config
	base   *url.URL
	client *http.Client
}

// NewS3 创建对象存储后端。使用 path-style 地址：endpoint/bucket/key。
func NewS3(cfg S3Config) (*Store, error) {
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 配置不完整：需要 endpoint、bucket、access key、secret key")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("S3 endpoint 无效：%q", cfg.Endpoint)
	}
	return &Store{b: &s3Backend{cfg: cfg, base: u, client: &http.Client{Timeout: 60 * time.Second}}}, nil
}

func (s *s3Backend) key(k string) string { return s.cfg.Prefix + k }

// uriEncode 按 SigV4 要求编码：除 A-Za-z0-9-._~ 外全部百分号编码；slash 为 false 时连 / 也编码。
func uriEncode(in string, slash bool) string {
	var b strings.Builder
	for _, c := range []byte(in) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// do 发起带 SigV4 签名的请求。bodySHA 是请求体的 sha256 十六进制。
func (s *s3Backend) do(method, key string, query url.Values, body []byte, bodySHA string) (*http.Response, error) {
	canonPath := uriEncode(strings.TrimRight(s.base.Path, "/")+"/"+s.cfg.Bucket+"/"+key, false)
	var qs []string
	for k, vs := range query {
		for _, v := range vs {
			qs = append(qs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(qs)
	canonQuery := strings.Join(qs, "&")
	u := *s.base
	u.Path, u.RawPath, u.RawQuery = "", "", ""
	target := u.String() + canonPath
	if canonQuery != "" {
		target += "?" + canonQuery
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")

	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		method, canonPath, canonQuery,
		"host:" + u.Host + "\nx-amz-content-sha256:" + bodySHA + "\nx-amz-date:" + amzDate + "\n",
		signed, bodySHA,
	}, "\n")
	scope := day + "/" + s.cfg.Region + "/s3/aws4_request"
	ch := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch[:])
	k := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), day)
	k = hmacSHA256(k, s.cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", bodySHA)
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signed, sig))
	return s.client.Do(req)
}

const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func respErr(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("对象存储 %s 失败：HTTP %d %s", op, resp.StatusCode, strings.TrimSpace(string(b)))
}

func (s *s3Backend) has(key string) bool {
	resp, err := s.do(http.MethodHead, s.key(key), nil, nil, emptySHA)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (s *s3Backend) put(key string, data []byte) error {
	resp, err := s.do(http.MethodPut, s.key(key), nil, data, Sum(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return respErr("PUT", resp)
	}
	return nil
}

func (s *s3Backend) read(key string) ([]byte, error) {
	resp, err := s.do(http.MethodGet, s.key(key), nil, nil, emptySHA)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		return nil, respErr("GET", resp)
	}
	return io.ReadAll(resp.Body)
}

func (s *s3Backend) del(key string) error {
	resp, err := s.do(http.MethodDelete, s.key(key), nil, nil, emptySHA)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return respErr("DELETE", resp)
	}
	return nil
}

func (s *s3Backend) list() ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if s.cfg.Prefix != "" {
			q.Set("prefix", s.cfg.Prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(http.MethodGet, "", q, nil, emptySHA)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			err := respErr("LIST", resp)
			resp.Body.Close()
			return nil, err
		}
		var res struct {
			Contents              []struct{ Key string }
			IsTruncated           bool
			NextContinuationToken string
		}
		err = xml.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, c := range res.Contents {
			keys = append(keys, strings.TrimPrefix(c.Key, s.cfg.Prefix))
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return keys, nil
		}
		token = res.NextContinuationToken
	}
}
