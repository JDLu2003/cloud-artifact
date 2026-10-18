package store

import (
	"database/sql"
	"errors"
)

type APIKey struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	UserName   string `json:"user_name,omitempty"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt *int64 `json:"last_used_at"`
	LastIP     string `json:"last_ip"`
	ExpiresAt  *int64 `json:"expires_at"`
	RevokedAt  *int64 `json:"revoked_at"`
}

func (k *APIKey) Active(at int64) bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && *k.ExpiresAt <= at {
		return false
	}
	return true
}

const keyCols = `k.id,k.user_id,u.name,k.name,k.prefix,k.created_at,k.last_used_at,k.last_ip,k.expires_at,k.revoked_at`

func scanKey(r scanner) (*APIKey, error) {
	k := &APIKey{}
	var lu, ex, rv sql.NullInt64
	if err := r.Scan(&k.ID, &k.UserID, &k.UserName, &k.Name, &k.Prefix, &k.CreatedAt, &lu, &k.LastIP, &ex, &rv); err != nil {
		return nil, err
	}
	if lu.Valid {
		k.LastUsedAt = &lu.Int64
	}
	if ex.Valid {
		k.ExpiresAt = &ex.Int64
	}
	if rv.Valid {
		k.RevokedAt = &rv.Int64
	}
	return k, nil
}

func (s *Store) CreateKey(userID int64, name, prefix, hash string, expiresAt *int64) (*APIKey, error) {
	res, err := s.DB.Exec(`INSERT INTO api_keys(user_id,name,prefix,hash,created_at,expires_at) VALUES(?,?,?,?,?,?)`,
		userID, name, prefix, hash, now(), expiresAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.KeyByID(id)
}

func (s *Store) KeyByID(id int64) (*APIKey, error) {
	k, err := scanKey(s.DB.QueryRow(`SELECT `+keyCols+` FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (s *Store) KeyByHash(hash string) (*APIKey, error) {
	k, err := scanKey(s.DB.QueryRow(`SELECT `+keyCols+` FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

// ListKeys 列出密钥；userID 为 0 时列出全部。
func (s *Store) ListKeys(userID int64) ([]APIKey, error) {
	q := `SELECT ` + keyCols + ` FROM api_keys k JOIN users u ON u.id=k.user_id`
	args := []any{}
	if userID > 0 {
		q += ` WHERE k.user_id=?`
		args = append(args, userID)
	}
	q += ` ORDER BY k.revoked_at IS NOT NULL, k.id DESC`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeKey(id int64) error {
	_, err := s.DB.Exec(`UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now(), id)
	return err
}

func (s *Store) TouchKey(id int64, ip string) {
	s.DB.Exec(`UPDATE api_keys SET last_used_at=?, last_ip=? WHERE id=?`, now(), ip, id)
}

func (s *Store) CountActiveKeys() int64 {
	var n int64
	s.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, now()).Scan(&n)
	return n
}

// ---- 会话 ----

type Session struct {
	UserID    int64
	ExpiresAt int64
}

func (s *Store) CreateSession(hash string, userID int64, expiresAt int64, ip, ua string) error {
	if len(ua) > 300 {
		ua = ua[:300]
	}
	_, err := s.DB.Exec(`INSERT INTO sessions(hash,user_id,created_at,expires_at,ip,ua) VALUES(?,?,?,?,?,?)`, hash, userID, now(), expiresAt, ip, ua)
	return err
}

func (s *Store) SessionByHash(hash string) (*Session, error) {
	ss := &Session{}
	err := s.DB.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE hash=?`, hash).Scan(&ss.UserID, &ss.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ss, err
}

func (s *Store) DeleteSession(hash string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE hash=?`, hash)
	return err
}

func (s *Store) PurgeExpiredSessions() {
	s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now())
}
