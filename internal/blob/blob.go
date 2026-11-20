// Package blob 存放 artifact 的文件。对象键按 artifact 组织：
//
//	<artifact_id>/v<版本号>/<文件路径>
//
// 每个版本都是一份完整快照，可以直接在对象存储里按目录浏览。
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// backend 是底层存储，本地目录和 S3 兼容对象存储（如 Cloudflare R2）各有一种实现。键以 / 分隔。
type backend interface {
	has(key string) bool
	put(key string, data []byte) error
	read(key string) ([]byte, error)
	del(key string) error
	list() ([]string, error)
}

// Loc 是一个文件在存储中的位置。
type Loc struct {
	ArtifactID string
	Version    int
	Path       string
}

func (l Loc) Key() string { return Key(l.ArtifactID, l.Version, l.Path) }

// Key 返回文件的对象键。
func Key(artifactID string, version int, p string) string {
	return artifactID + "/v" + strconv.Itoa(version) + "/" + p
}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9]{1,32}/v[0-9]+/.+$`)

// IsArtifactKey 判断键是否符合当前布局；GC 只处理这类键，不碰其他对象。
func IsArtifactKey(key string) bool { return keyRe.MatchString(key) }

var legacyRe = regexp.MustCompile(`^[0-9a-f]{2}/[0-9a-f]{62}$`)

// IsLegacyKey 判断键是否是旧版按 sha256 拆分的布局（ab/cdef…）。
func IsLegacyKey(key string) bool { return legacyRe.MatchString(key) }

func validKey(key string) error {
	if !IsArtifactKey(key) || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return fmt.Errorf("invalid key: %q", key)
	}
	return nil
}

type Store struct {
	b      backend
	locate func(sha string) (Loc, bool)
	fetch  func(key string) ([]byte, error) // 本地缺失时从别处（其他机器、R2 备份）取回
}

// SetFetcher 设置本地缺失时的取回函数；取回的内容会写入本地。
func (s *Store) SetFetcher(fn func(key string) ([]byte, error)) { s.fetch = fn }

// readThrough 先读本地，缺失时尝试取回并保存。
func (s *Store) readThrough(key string) ([]byte, error) {
	b, err := s.b.read(key)
	if err == nil || !errors.Is(err, os.ErrNotExist) || s.fetch == nil {
		return b, err
	}
	b, ferr := s.fetch(key)
	if ferr != nil {
		return nil, os.ErrNotExist
	}
	if validKey(key) == nil {
		_ = s.b.put(key, b)
	}
	return b, nil
}

// ReadLocal 只读本地，不尝试取回（供其他机器拉取时使用，避免互相转发形成环路）。
func (s *Store) ReadLocal(key string) ([]byte, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	return s.b.read(key)
}

// Age 返回本地文件的存在时长；后端不支持或文件不存在时 ok=false。
func (s *Store) Age(key string) (time.Duration, bool) {
	l, ok := s.b.(*localBackend)
	if !ok {
		return 0, false
	}
	fi, err := os.Stat(l.path(key))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

// SetLocator 设置按 sha256 查文件位置的函数（由数据库提供）。
// 内容相同的文件在任何位置字节都一样，所以任取一处即可。
func (s *Store) SetLocator(fn func(sha string) (Loc, bool)) { s.locate = fn }

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

// PutAt 把文件写到 <artifact_id>/v<version>/<path>。
func (s *Store) PutAt(l Loc, data []byte) error {
	if err := validKey(l.Key()); err != nil {
		return err
	}
	return s.b.put(l.Key(), data)
}

// PutKey 按对象键写入（其他机器推送文件时使用）。
func (s *Store) PutKey(key string, data []byte) error {
	if err := validKey(key); err != nil {
		return err
	}
	return s.b.put(key, data)
}

// HasKey 判断对象是否存在。
func (s *Store) HasKey(key string) bool { return s.b.has(key) }

// ReadKey 按对象键读取。
func (s *Store) ReadKey(key string) ([]byte, error) { return s.readThrough(key) }

// Read 按内容的 sha256 读取文件。
func (s *Store) Read(sha string) ([]byte, error) {
	if s.locate == nil {
		return nil, errors.New("blob: 未设置 locator")
	}
	l, ok := s.locate(sha)
	if !ok {
		return nil, os.ErrNotExist
	}
	return s.readThrough(l.Key())
}

// DeleteKey 删除一个对象；不存在时不报错。
func (s *Store) DeleteKey(key string) error {
	if err := validKey(key); err != nil && !IsLegacyKey(key) {
		return err
	}
	return s.b.del(key)
}

// List 列出存储中所有对象的键。
func (s *Store) List() ([]string, error) { return s.b.list() }

// Shared 表示文件存在所有机器共用的对象存储上（R2）。
func (s *Store) Shared() bool {
	_, ok := s.b.(*s3Backend)
	return ok
}

// 内容分发用的对象（只在对象存储上）：
//
//	_m/<id>.json, _m/<id>/v<版本>.json, _m/_settings.json   访问控制与文件清单
//	_r/<id>/v<版本>/<路径>                                 预先渲染好的 HTML
//
// 这些键以 _ 开头，不符合 artifact 文件的键格式，所以 GC 不会碰它们。
var edgeKeyRe = regexp.MustCompile(`^_[mr]/[A-Za-z0-9_]{1,32}(\.json|/.+)$`)

func validEdgeKey(key string) error {
	if !edgeKeyRe.MatchString(key) || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return fmt.Errorf("invalid edge key: %q", key)
	}
	return nil
}

// PutEdge 写入一个内容分发对象（只在对象存储模式下可用）。
func (s *Store) PutEdge(key string, data []byte, contentType string) error {
	b, ok := s.b.(*s3Backend)
	if !ok {
		return errors.New("blob: 内容分发对象只能写入对象存储")
	}
	if err := validEdgeKey(key); err != nil {
		return err
	}
	return b.putType(key, data, contentType)
}

// DeleteEdgePrefix 删除某个前缀下的所有内容分发对象（例如 _r/<id>/）。
func (s *Store) DeleteEdgePrefix(prefix string) error {
	b, ok := s.b.(*s3Backend)
	if !ok || !strings.HasPrefix(prefix, "_") {
		return nil
	}
	keys, err := b.listPrefix(prefix)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if validEdgeKey(k) == nil {
			if err := b.del(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteEdge 删除一个内容分发对象；不存在时不报错。
func (s *Store) DeleteEdge(key string) error {
	b, ok := s.b.(*s3Backend)
	if !ok {
		return nil
	}
	if err := validEdgeKey(key); err != nil {
		return err
	}
	return b.del(key)
}

type localBackend struct{ dir string }

func (l *localBackend) path(key string) string { return filepath.Join(l.dir, filepath.FromSlash(key)) }

func (l *localBackend) has(key string) bool {
	_, err := os.Stat(l.path(key))
	return err == nil
}

func (l *localBackend) put(key string, data []byte) error {
	p := l.path(key)
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

func (l *localBackend) read(key string) ([]byte, error) { return os.ReadFile(l.path(key)) }

func (l *localBackend) del(key string) error {
	err := os.Remove(l.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *localBackend) list() ([]string, error) {
	var out []string
	err := filepath.WalkDir(l.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, _ := filepath.Rel(l.dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}
