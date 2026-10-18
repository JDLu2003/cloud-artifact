// Package store 封装 SQLite 数据访问。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	DB   *sql.DB
	Path string

	settingsMu    sync.RWMutex
	settingsCache *Settings
}

func now() int64 { return time.Now().Unix() }

// Open 打开（必要时创建）数据库并执行迁移。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "artifact.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(8000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{DB: db, Path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库迁移失败：%w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

var migrations = []string{
	// v1
	`
CREATE TABLE users(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'user',
  disabled INTEGER NOT NULL DEFAULT 0,
  quota_storage_mb INTEGER,
  quota_artifacts INTEGER,
  quota_file_mb INTEGER,
  quota_req_per_min INTEGER,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE api_keys(
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  last_ip TEXT NOT NULL DEFAULT '',
  expires_at INTEGER,
  revoked_at INTEGER
);
CREATE INDEX api_keys_user ON api_keys(user_id);
CREATE TABLE sessions(
  hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ip TEXT NOT NULL DEFAULT '',
  ua TEXT NOT NULL DEFAULT ''
);
CREATE TABLE artifacts(
  id TEXT PRIMARY KEY,
  owner_id INTEGER NOT NULL REFERENCES users(id),
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'html',
  entry TEXT NOT NULL DEFAULT 'index.html',
  visibility TEXT NOT NULL DEFAULT 'unlisted',
  current_version INTEGER NOT NULL DEFAULT 0,
  views INTEGER NOT NULL DEFAULT 0,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  deleted_at INTEGER
);
CREATE INDEX artifacts_owner ON artifacts(owner_id, updated_at);
CREATE INDEX artifacts_updated ON artifacts(updated_at);
CREATE TABLE versions(
  artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'publish',
  entry TEXT NOT NULL,
  author_user_id INTEGER,
  author_key_id INTEGER,
  total_size INTEGER NOT NULL DEFAULT 0,
  file_count INTEGER NOT NULL DEFAULT 0,
  warnings INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(artifact_id, number)
);
CREATE TABLE version_files(
  artifact_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  mime TEXT NOT NULL,
  PRIMARY KEY(artifact_id, version, path),
  FOREIGN KEY(artifact_id, version) REFERENCES versions(artifact_id, number) ON DELETE CASCADE
);
CREATE INDEX version_files_sha ON version_files(sha256);
CREATE TABLE blobs(
  sha256 TEXT PRIMARY KEY,
  size INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE VIRTUAL TABLE artifacts_fts USING fts5(id UNINDEXED, title, description, body, tokenize='trigram');
CREATE TABLE audit_log(
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  user_id INTEGER,
  key_id INTEGER,
  ip TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_ts ON audit_log(ts);
CREATE INDEX audit_user ON audit_log(user_id, ts);
CREATE TABLE kv(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`,
}

func (s *Store) migrate() error {
	var ver int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return err
	}
	for i := ver; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx 在一个写事务中执行 fn。
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// KV 读写简单键值（服务密钥、设置等）。
func (s *Store) GetKV(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetKV(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// DBSize 返回数据库文件（含 WAL）占用的字节数。
func (s *Store) DBSize() int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(s.Path + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}
