package store

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

type Artifact struct {
	ID             string `json:"id"`
	OwnerID        int64  `json:"owner_id"`
	OwnerName      string `json:"owner_name"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Kind           string `json:"kind"`
	Entry          string `json:"entry"`
	Visibility     string `json:"visibility"`
	CurrentVersion int    `json:"current_version"`
	Views          int64  `json:"views"`
	SizeBytes      int64  `json:"size_bytes"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	DeletedAt      *int64 `json:"deleted_at,omitempty"`
}

type Version struct {
	ArtifactID   string `json:"-"`
	Number       int    `json:"number"`
	Message      string `json:"message"`
	Source       string `json:"source"`
	Entry        string `json:"entry"`
	AuthorUserID *int64 `json:"author_user_id,omitempty"`
	AuthorName   string `json:"author_name,omitempty"`
	AuthorKeyID  *int64 `json:"author_key_id,omitempty"`
	TotalSize    int64  `json:"total_size"`
	FileCount    int    `json:"file_count"`
	Warnings     int    `json:"warnings"`
	CreatedAt    int64  `json:"created_at"`
}

type VersionFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mime   string `json:"mime"`
}

const artCols = `a.id,a.owner_id,u.name,a.title,a.description,a.kind,a.entry,a.visibility,a.current_version,a.views,a.size_bytes,a.created_at,a.updated_at,a.deleted_at`

func scanArtifact(r scanner) (*Artifact, error) {
	a := &Artifact{}
	var del sql.NullInt64
	if err := r.Scan(&a.ID, &a.OwnerID, &a.OwnerName, &a.Title, &a.Description, &a.Kind, &a.Entry, &a.Visibility, &a.CurrentVersion, &a.Views, &a.SizeBytes, &a.CreatedAt, &a.UpdatedAt, &del); err != nil {
		return nil, err
	}
	if del.Valid {
		a.DeletedAt = &del.Int64
	}
	return a, nil
}

func (s *Store) ArtifactByID(id string) (*Artifact, error) {
	a, err := scanArtifact(s.DB.QueryRow(`SELECT `+artCols+` FROM artifacts a JOIN users u ON u.id=a.owner_id WHERE a.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) ArtifactExists(id string) bool {
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE id=?`, id).Scan(&n)
	return n > 0
}

func (s *Store) CountArtifacts(ownerID int64) int64 {
	var n int64
	s.DB.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE owner_id=? AND deleted_at IS NULL`, ownerID).Scan(&n)
	return n
}

type ListQuery struct {
	OwnerID    int64  // 0 = 不限
	Query      string // 全文检索
	Visibility string
	Kind       string
	Deleted    bool // true 只列回收站
	Sort       string
	Limit      int
	Offset     int
}

type ListResult struct {
	Items []Artifact `json:"items"`
	Total int64      `json:"total"`
}

func (s *Store) ListArtifacts(q ListQuery) (*ListResult, error) {
	where := []string{}
	args := []any{}
	if q.Deleted {
		where = append(where, "a.deleted_at IS NOT NULL")
	} else {
		where = append(where, "a.deleted_at IS NULL")
	}
	if q.OwnerID > 0 {
		where = append(where, "a.owner_id=?")
		args = append(args, q.OwnerID)
	}
	if q.Visibility != "" {
		where = append(where, "a.visibility=?")
		args = append(args, q.Visibility)
	}
	if q.Kind != "" {
		where = append(where, "a.kind=?")
		args = append(args, q.Kind)
	}
	if t := strings.TrimSpace(q.Query); t != "" {
		if utf8.RuneCountInString(t) >= 3 {
			where = append(where, "a.id IN (SELECT id FROM artifacts_fts WHERE artifacts_fts MATCH ?)")
			args = append(args, ftsQuote(t))
		} else {
			// trigram 至少需要 3 个字符，短词退回 LIKE
			like := "%" + escapeLike(t) + "%"
			where = append(where, `(a.title LIKE ? ESCAPE '\' OR a.description LIKE ? ESCAPE '\' OR a.id = ? OR a.id IN (SELECT id FROM artifacts_fts WHERE body LIKE ? ESCAPE '\'))`)
			args = append(args, like, like, t, like)
		}
	}
	w := " WHERE " + strings.Join(where, " AND ")
	order := " ORDER BY a.updated_at DESC"
	switch q.Sort {
	case "created":
		order = " ORDER BY a.created_at DESC"
	case "views":
		order = " ORDER BY a.views DESC"
	case "size":
		order = " ORDER BY a.size_bytes DESC"
	case "title":
		order = " ORDER BY a.title COLLATE NOCASE"
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	res := &ListResult{Items: []Artifact{}}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM artifacts a`+w, args...).Scan(&res.Total); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT `+artCols+` FROM artifacts a JOIN users u ON u.id=a.owner_id`+w+order+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, *a)
	}
	return res, rows.Err()
}

func ftsQuote(s string) string {
	// 作为短语整体匹配，避免用户输入被解释成 FTS 语法
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ---- 写入（在事务中调用） ----

func InsertArtifact(tx *sql.Tx, a *Artifact) error {
	_, err := tx.Exec(`INSERT INTO artifacts(id,owner_id,title,description,kind,entry,visibility,current_version,size_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.OwnerID, a.Title, a.Description, a.Kind, a.Entry, a.Visibility, a.CurrentVersion, a.SizeBytes, a.CreatedAt, a.UpdatedAt)
	return err
}

func UpdateArtifactHead(tx *sql.Tx, a *Artifact) error {
	_, err := tx.Exec(`UPDATE artifacts SET title=?,description=?,kind=?,entry=?,visibility=?,current_version=?,size_bytes=?,updated_at=? WHERE id=?`,
		a.Title, a.Description, a.Kind, a.Entry, a.Visibility, a.CurrentVersion, a.SizeBytes, a.UpdatedAt, a.ID)
	return err
}

func InsertVersion(tx *sql.Tx, v *Version, files []VersionFile) error {
	if _, err := tx.Exec(`INSERT INTO versions(artifact_id,number,message,source,entry,author_user_id,author_key_id,total_size,file_count,warnings,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		v.ArtifactID, v.Number, v.Message, v.Source, v.Entry, v.AuthorUserID, v.AuthorKeyID, v.TotalSize, v.FileCount, v.Warnings, v.CreatedAt); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO version_files(artifact_id,version,path,sha256,size,mime) VALUES(?,?,?,?,?,?)`, v.ArtifactID, v.Number, f.Path, f.SHA256, f.Size, f.Mime); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO blobs(sha256,size,created_at) VALUES(?,?,?)`, f.SHA256, f.Size, now()); err != nil {
			return err
		}
	}
	return nil
}

func UpsertFTS(tx *sql.Tx, id, title, desc, body string) error {
	if _, err := tx.Exec(`DELETE FROM artifacts_fts WHERE id=?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO artifacts_fts(id,title,description,body) VALUES(?,?,?,?)`, id, title, desc, body)
	return err
}

// PruneVersions 删除超出保留数量的最旧版本（不删当前版本）。
func PruneVersions(tx *sql.Tx, artifactID string, keep int64) error {
	if keep <= 0 {
		return nil
	}
	_, err := tx.Exec(`DELETE FROM versions WHERE artifact_id=? AND number NOT IN (SELECT number FROM versions WHERE artifact_id=? ORDER BY number DESC LIMIT ?)`, artifactID, artifactID, keep)
	return err
}

func (s *Store) UpdateMeta(id string, title, desc, visibility *string, ownerID *int64) error {
	sets := []string{}
	args := []any{}
	if title != nil {
		sets = append(sets, "title=?")
		args = append(args, *title)
	}
	if desc != nil {
		sets = append(sets, "description=?")
		args = append(args, *desc)
	}
	if visibility != nil {
		sets = append(sets, "visibility=?")
		args = append(args, *visibility)
	}
	if ownerID != nil {
		sets = append(sets, "owner_id=?")
		args = append(args, *ownerID)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now(), id)
	if _, err := s.DB.Exec(`UPDATE artifacts SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return err
	}
	if title != nil || desc != nil {
		s.DB.Exec(`UPDATE artifacts_fts SET title=(SELECT title FROM artifacts WHERE id=?), description=(SELECT description FROM artifacts WHERE id=?) WHERE id=?`, id, id, id)
	}
	return nil
}

func (s *Store) SoftDelete(id string) error {
	_, err := s.DB.Exec(`UPDATE artifacts SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, now(), id)
	return err
}

func (s *Store) Undelete(id string) error {
	_, err := s.DB.Exec(`UPDATE artifacts SET deleted_at=NULL, updated_at=? WHERE id=?`, now(), id)
	return err
}

// Purge 彻底删除 artifact 及其所有版本记录（blob 文件留给 GC）。
func (s *Store) Purge(id string) error {
	return s.Tx(nil, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM artifacts_fts WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM version_files WHERE artifact_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM versions WHERE artifact_id=?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM artifacts WHERE id=?`, id)
		return err
	})
}

func (s *Store) ExpiredTrash(days int64) []string {
	if days <= 0 {
		return nil
	}
	rows, err := s.DB.Query(`SELECT id FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at < ?`, now()-days*86400)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func (s *Store) ArtifactIDsByOwner(ownerID int64) []string {
	rows, err := s.DB.Query(`SELECT id FROM artifacts WHERE owner_id=?`, ownerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func (s *Store) IncViews(id string) {
	s.DB.Exec(`UPDATE artifacts SET views=views+1 WHERE id=?`, id)
}

// ---- 版本 ----

func (s *Store) Versions(artifactID string) ([]Version, error) {
	rows, err := s.DB.Query(`SELECT v.number,v.message,v.source,v.entry,v.author_user_id,COALESCE(u.name,''),v.author_key_id,v.total_size,v.file_count,v.warnings,v.created_at
		FROM versions v LEFT JOIN users u ON u.id=v.author_user_id WHERE v.artifact_id=? ORDER BY v.number DESC`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v := Version{ArtifactID: artifactID}
		var au, ak sql.NullInt64
		if err := rows.Scan(&v.Number, &v.Message, &v.Source, &v.Entry, &au, &v.AuthorName, &ak, &v.TotalSize, &v.FileCount, &v.Warnings, &v.CreatedAt); err != nil {
			return nil, err
		}
		if au.Valid {
			v.AuthorUserID = &au.Int64
		}
		if ak.Valid {
			v.AuthorKeyID = &ak.Int64
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) VersionByNumber(artifactID string, n int) (*Version, error) {
	v := &Version{ArtifactID: artifactID}
	var au, ak sql.NullInt64
	err := s.DB.QueryRow(`SELECT v.number,v.message,v.source,v.entry,v.author_user_id,COALESCE(u.name,''),v.author_key_id,v.total_size,v.file_count,v.warnings,v.created_at
		FROM versions v LEFT JOIN users u ON u.id=v.author_user_id WHERE v.artifact_id=? AND v.number=?`, artifactID, n).
		Scan(&v.Number, &v.Message, &v.Source, &v.Entry, &au, &v.AuthorName, &ak, &v.TotalSize, &v.FileCount, &v.Warnings, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if au.Valid {
		v.AuthorUserID = &au.Int64
	}
	if ak.Valid {
		v.AuthorKeyID = &ak.Int64
	}
	return v, err
}

func (s *Store) VersionFiles(artifactID string, n int) ([]VersionFile, error) {
	rows, err := s.DB.Query(`SELECT path,sha256,size,mime FROM version_files WHERE artifact_id=? AND version=? ORDER BY path`, artifactID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VersionFile{}
	for rows.Next() {
		var f VersionFile
		if err := rows.Scan(&f.Path, &f.SHA256, &f.Size, &f.Mime); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) VersionFile(artifactID string, n int, path string) (*VersionFile, error) {
	f := &VersionFile{}
	err := s.DB.QueryRow(`SELECT path,sha256,size,mime FROM version_files WHERE artifact_id=? AND version=? AND path=?`, artifactID, n, path).Scan(&f.Path, &f.SHA256, &f.Size, &f.Mime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// ---- blob 与统计 ----

// UnreferencedBlobs 返回没有任何版本引用的 blob。
func (s *Store) UnreferencedBlobs() ([]VersionFile, error) {
	rows, err := s.DB.Query(`SELECT b.sha256, b.size FROM blobs b WHERE NOT EXISTS (SELECT 1 FROM version_files vf WHERE vf.sha256=b.sha256)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionFile
	for rows.Next() {
		var f VersionFile
		rows.Scan(&f.SHA256, &f.Size)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBlobRow(sha string) error {
	_, err := s.DB.Exec(`DELETE FROM blobs WHERE sha256=?`, sha)
	return err
}

type Counts struct {
	Users          int64 `json:"users"`
	ActiveKeys     int64 `json:"active_keys"`
	Artifacts      int64 `json:"artifacts"`
	Trashed        int64 `json:"trashed"`
	Versions       int64 `json:"versions"`
	Blobs          int64 `json:"blobs"`
	BlobBytes      int64 `json:"blob_bytes"`
	TotalViews     int64 `json:"total_views"`
	PublishesToday int64 `json:"publishes_today"`
}

func (s *Store) Counts() Counts {
	var c Counts
	s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&c.Users)
	c.ActiveKeys = s.CountActiveKeys()
	s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(views),0) FROM artifacts WHERE deleted_at IS NULL`).Scan(&c.Artifacts, &c.TotalViews)
	s.DB.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE deleted_at IS NOT NULL`).Scan(&c.Trashed)
	s.DB.QueryRow(`SELECT COUNT(*) FROM versions`).Scan(&c.Versions)
	s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0) FROM blobs`).Scan(&c.Blobs, &c.BlobBytes)
	s.DB.QueryRow(`SELECT COUNT(*) FROM versions WHERE created_at >= ?`, now()-86400).Scan(&c.PublishesToday)
	return c
}

type RecentVersion struct {
	ArtifactID string `json:"artifact_id"`
	Title      string `json:"title"`
	Number     int    `json:"number"`
	Source     string `json:"source"`
	Message    string `json:"message"`
	Author     string `json:"author"`
	Warnings   int    `json:"warnings"`
	CreatedAt  int64  `json:"created_at"`
}

func (s *Store) RecentVersions(ownerID int64, limit int) []RecentVersion {
	q := `SELECT v.artifact_id, a.title, v.number, v.source, v.message, COALESCE(u.name,''), v.warnings, v.created_at
		FROM versions v JOIN artifacts a ON a.id=v.artifact_id LEFT JOIN users u ON u.id=v.author_user_id WHERE a.deleted_at IS NULL`
	args := []any{}
	if ownerID > 0 {
		q += ` AND a.owner_id=?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY v.created_at DESC, v.number DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	out := []RecentVersion{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var r RecentVersion
		rows.Scan(&r.ArtifactID, &r.Title, &r.Number, &r.Source, &r.Message, &r.Author, &r.Warnings, &r.CreatedAt)
		out = append(out, r)
	}
	return out
}

// PublishesPerDay 返回最近 days 天每天的版本数（按 UTC+8 自然日）。
func (s *Store) PublishesPerDay(days int, ownerID int64) []int64 {
	out := make([]int64, days)
	start := (now()+8*3600)/86400*86400 - 8*3600 - int64(days-1)*86400
	q := `SELECT (v.created_at - ?) / 86400, COUNT(*) FROM versions v`
	args := []any{start}
	if ownerID > 0 {
		q += ` JOIN artifacts a ON a.id=v.artifact_id WHERE a.owner_id=? AND v.created_at >= ?`
		args = append(args, ownerID, start)
	} else {
		q += ` WHERE v.created_at >= ?`
		args = append(args, start)
	}
	q += ` GROUP BY 1`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var d, n int64
		rows.Scan(&d, &n)
		if d >= 0 && d < int64(days) {
			out[d] = n
		}
	}
	return out
}
