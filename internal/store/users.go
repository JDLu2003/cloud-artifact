package store

import (
	"database/sql"
	"errors"
	"strings"
)

type User struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	PasswordHash   string `json:"-"`
	Role           string `json:"role"`
	Disabled       bool   `json:"disabled"`
	QuotaStorageMB *int64 `json:"quota_storage_mb"`
	QuotaArtifacts *int64 `json:"quota_artifacts"`
	QuotaFileMB    *int64 `json:"quota_file_mb"`
	QuotaReqPerMin *int64 `json:"quota_req_per_min"`
	Note           string `json:"note"`
	CreatedAt      int64  `json:"created_at"`
	HasPassword    bool   `json:"has_password"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// Quota 是某个用户的实际限额（个人覆盖值优先，否则取全局默认值）。
type Quota struct {
	StorageBytes  int64 `json:"storage_bytes"`
	Artifacts     int64 `json:"artifacts"`
	FileBytes     int64 `json:"file_bytes"`
	VersionBytes  int64 `json:"version_bytes"`
	Files         int64 `json:"files"`
	ReqPerMin     int64 `json:"req_per_min"`
	PublishPerMin int64 `json:"publish_per_min"`
}

const mb = 1 << 20

func (u *User) Quota(st Settings) Quota {
	q := Quota{
		StorageBytes:  st.QuotaStorageMB * mb,
		Artifacts:     st.QuotaArtifacts,
		FileBytes:     st.QuotaFileMB * mb,
		VersionBytes:  st.QuotaVersionMB * mb,
		Files:         st.QuotaFiles,
		ReqPerMin:     st.QuotaReqPerMin,
		PublishPerMin: st.QuotaPublishPerMin,
	}
	if u.QuotaStorageMB != nil {
		q.StorageBytes = *u.QuotaStorageMB * mb
	}
	if u.QuotaArtifacts != nil {
		q.Artifacts = *u.QuotaArtifacts
	}
	if u.QuotaFileMB != nil {
		q.FileBytes = *u.QuotaFileMB * mb
		if q.FileBytes > q.VersionBytes {
			q.VersionBytes = q.FileBytes
		}
	}
	if u.QuotaReqPerMin != nil {
		q.ReqPerMin = *u.QuotaReqPerMin
	}
	return q
}

const userCols = `id,name,display_name,password_hash,role,disabled,quota_storage_mb,quota_artifacts,quota_file_mb,quota_req_per_min,note,created_at`

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (*User, error) {
	u := &User{}
	var qs, qa, qf, qr sql.NullInt64
	var disabled int
	if err := r.Scan(&u.ID, &u.Name, &u.DisplayName, &u.PasswordHash, &u.Role, &disabled, &qs, &qa, &qf, &qr, &u.Note, &u.CreatedAt); err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	u.HasPassword = u.PasswordHash != ""
	if qs.Valid {
		u.QuotaStorageMB = &qs.Int64
	}
	if qa.Valid {
		u.QuotaArtifacts = &qa.Int64
	}
	if qf.Valid {
		u.QuotaFileMB = &qf.Int64
	}
	if qr.Valid {
		u.QuotaReqPerMin = &qr.Int64
	}
	return u, nil
}

func (s *Store) UserByID(id int64) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByName(name string) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE name=?`, strings.TrimSpace(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CountUsers() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// UserRow 是用户列表中附带用量统计的一行。
type UserRow struct {
	User
	ArtifactCount int64 `json:"artifact_count"`
	StorageBytes  int64 `json:"storage_bytes"`
	KeyCount      int64 `json:"key_count"`
	LastActiveAt  int64 `json:"last_active_at"`
}

func (s *Store) ListUsers() ([]UserRow, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, UserRow{User: *u})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		id := out[i].ID
		s.DB.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE owner_id=? AND deleted_at IS NULL`, id).Scan(&out[i].ArtifactCount)
		out[i].StorageBytes, _ = s.UserStorage(id)
		s.DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(last_used_at),0) FROM api_keys WHERE user_id=? AND revoked_at IS NULL`, id).Scan(&out[i].KeyCount, &out[i].LastActiveAt)
	}
	return out, nil
}

type NewUser struct {
	Name         string
	DisplayName  string
	PasswordHash string
	Role         string
	Note         string
}

func (s *Store) CreateUser(n NewUser) (*User, error) {
	if n.Role == "" {
		n.Role = "user"
	}
	res, err := s.DB.Exec(`INSERT INTO users(name,display_name,password_hash,role,note,created_at) VALUES(?,?,?,?,?,?)`,
		strings.TrimSpace(n.Name), n.DisplayName, n.PasswordHash, n.Role, n.Note, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("用户名已存在")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.UserByID(id)
}

// UserPatch 中为 nil 的字段不修改；Clear* 为 true 时把对应限额恢复为默认值。
type UserPatch struct {
	DisplayName    *string  `json:"display_name"`
	Role           *string  `json:"role"`
	Disabled       *bool    `json:"disabled"`
	Note           *string  `json:"note"`
	QuotaStorageMB *int64   `json:"quota_storage_mb"`
	QuotaArtifacts *int64   `json:"quota_artifacts"`
	QuotaFileMB    *int64   `json:"quota_file_mb"`
	QuotaReqPerMin *int64   `json:"quota_req_per_min"`
	ClearQuotas    []string `json:"clear_quotas"`
}

func (s *Store) UpdateUser(id int64, p UserPatch) error {
	sets := []string{}
	args := []any{}
	add := func(col string, v any) {
		sets = append(sets, col+"=?")
		args = append(args, v)
	}
	if p.DisplayName != nil {
		add("display_name", *p.DisplayName)
	}
	if p.Role != nil {
		if *p.Role != "admin" && *p.Role != "user" {
			return errors.New("角色必须是 admin 或 user")
		}
		add("role", *p.Role)
	}
	if p.Disabled != nil {
		add("disabled", boolInt(*p.Disabled))
	}
	if p.Note != nil {
		add("note", *p.Note)
	}
	for col, v := range map[string]*int64{"quota_storage_mb": p.QuotaStorageMB, "quota_artifacts": p.QuotaArtifacts, "quota_file_mb": p.QuotaFileMB, "quota_req_per_min": p.QuotaReqPerMin} {
		if v != nil {
			if *v <= 0 {
				return errors.New("限额必须大于 0")
			}
			add(col, *v)
		}
	}
	for _, c := range p.ClearQuotas {
		switch c {
		case "quota_storage_mb", "quota_artifacts", "quota_file_mb", "quota_req_per_min":
			sets = append(sets, c+"=NULL")
		}
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.DB.Exec(`UPDATE users SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

func (s *Store) SetPassword(id int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, id)
	if err == nil {
		// 改密码后让所有旧会话失效
		_, err = s.DB.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	}
	return err
}

func (s *Store) CountAdmins() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

// DeleteUser 删除用户及其密钥、会话。调用方需先清理该用户的 artifact。
func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UserStorage 计算用户占用的存储（同一用户内按内容去重）。
func (s *Store) UserStorage(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(size),0) FROM (
		SELECT DISTINCT vf.sha256, vf.size FROM version_files vf
		JOIN artifacts a ON a.id = vf.artifact_id WHERE a.owner_id=?)`, userID).Scan(&n)
	return n, err
}

// UserHasBlob 判断用户是否已经引用过某个 blob（用于计算新增占用）。
func (s *Store) UserBlobSet(userID int64) (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT DISTINCT vf.sha256 FROM version_files vf JOIN artifacts a ON a.id=vf.artifact_id WHERE a.owner_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		m[sha] = true
	}
	return m, rows.Err()
}
