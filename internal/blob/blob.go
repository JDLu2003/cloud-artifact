// Package blob 实现按 sha256 寻址的文件存储。同样的内容只存一份。
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type Store struct {
	dir string
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Store) path(sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("invalid sha256: %q", sha)
	}
	return filepath.Join(s.dir, sha[:2], sha[2:]), nil
}

func (s *Store) Has(sha string) bool {
	p, err := s.path(sha)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Put 写入内容并返回 sha256；内容已存在时不重复写。
func (s *Store) Put(data []byte) (string, error) {
	sha := Sum(data)
	p, _ := s.path(sha)
	if _, err := os.Stat(p); err == nil {
		return sha, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return sha, nil
}

func (s *Store) Open(sha string) (*os.File, error) {
	p, err := s.path(sha)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (s *Store) Read(sha string) ([]byte, error) {
	f, err := s.Open(sha)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (s *Store) Delete(sha string) error {
	p, err := s.path(sha)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
