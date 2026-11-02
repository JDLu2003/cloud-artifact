// Package blob 实现按 sha256 寻址的文件存储。同样的内容只存一份。
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// backend 是底层存储，本地目录和 S3 兼容对象存储（如 Cloudflare R2）各有一种实现。
type backend interface {
	has(sha string) bool
	put(sha string, data []byte) error
	read(sha string) ([]byte, error)
	del(sha string) error
}

type Store struct {
	b backend
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// New 创建本地目录后端。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{b: &localBackend{dir: dir}}, nil
}

func Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func checkSHA(sha string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("invalid sha256: %q", sha)
	}
	return nil
}

func (s *Store) Has(sha string) bool {
	if checkSHA(sha) != nil {
		return false
	}
	return s.b.has(sha)
}

// Put 写入内容并返回 sha256；内容已存在时不重复写。
func (s *Store) Put(data []byte) (string, error) {
	sha := Sum(data)
	if s.b.has(sha) {
		return sha, nil
	}
	if err := s.b.put(sha, data); err != nil {
		return "", err
	}
	return sha, nil
}

func (s *Store) Read(sha string) ([]byte, error) {
	if err := checkSHA(sha); err != nil {
		return nil, err
	}
	return s.b.read(sha)
}

func (s *Store) Delete(sha string) error {
	if err := checkSHA(sha); err != nil {
		return err
	}
	return s.b.del(sha)
}

type localBackend struct{ dir string }

func (l *localBackend) path(sha string) string {
	return filepath.Join(l.dir, sha[:2], sha[2:])
}

func (l *localBackend) has(sha string) bool {
	_, err := os.Stat(l.path(sha))
	return err == nil
}

func (l *localBackend) put(sha string, data []byte) error {
	p := l.path(sha)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (l *localBackend) read(sha string) ([]byte, error) { return os.ReadFile(l.path(sha)) }

func (l *localBackend) del(sha string) error {
	err := os.Remove(l.path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
