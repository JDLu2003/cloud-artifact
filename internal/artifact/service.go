// Package artifact 实现 artifact 的发布、版本管理、局部编辑、回滚和对比。
package artifact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"vps-artifact/internal/auth"
	"vps-artifact/internal/blob"
	"vps-artifact/internal/config"
	"vps-artifact/internal/lint"
	"vps-artifact/internal/store"
)

// Error 携带 HTTP 状态码和机器可读的错误码。
type Error struct {
	Status int            `json:"-"`
	Code   string         `json:"code"`
	Msg    string         `json:"message"`
	Data   map[string]any `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Msg }

func errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

type Service struct {
	Store     *store.Store
	Blobs     *blob.Store
	Cfg       *config.Config
	Signer    *auth.Signer
	LibExists func(string) bool

	gcMu sync.RWMutex // 发布时读锁，GC 时写锁，避免刚写入的 blob 被回收
}

// Actor 是发起操作的身份。
type Actor struct {
	User  *store.User
	KeyID *int64
	IP    string
}

func (a Actor) uid() *int64 {
	if a.User == nil {
		return nil
	}
	return &a.User.ID
}

// FileInput 是一次发布中的单个文件：要么给内容，要么引用已有 blob（回滚时使用）。
type FileInput struct {
	Content []byte
	Ref     *store.VersionFile
}

type PublishInput struct {
	ArtifactID  string
	BaseVersion *int
	Files       map[string]*FileInput // 值为 nil 表示删除该文件
	Replace     bool                  // true：未提供的旧文件全部删除
	Title       *string
	Description *string
	Visibility  *string
	Entry       *string
	Message     string
	Source      string // publish | edit | restore
	Actor       Actor
}

type PublishResult struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	URL        string         `json:"url"`
	RawURL     string         `json:"raw_url"`
	Title      string         `json:"title"`
	Visibility string         `json:"visibility"`
	Entry      string         `json:"entry"`
	Kind       string         `json:"kind"`
	Files      int            `json:"files"`
	Size       int64          `json:"size"`
	Created    bool           `json:"created"`
	Unchanged  bool           `json:"unchanged"`
	Warnings   []lint.Warning `json:"warnings"`
}

func (s *Service) ViewURL(id string) string { return s.Cfg.PublicURL + "/a/" + id }

func canWrite(a Actor, art *store.Artifact) bool {
	return a.User != nil && (a.User.IsAdmin() || a.User.ID == art.OwnerID)
}

func validVisibility(v string) bool { return v == "private" || v == "unlisted" || v == "public" }

// Load 读取 artifact 并检查调用方是否有写权限。
func (s *Service) Load(a Actor, id string, write bool) (*store.Artifact, error) {
	art, err := s.Store.ArtifactByID(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(404, "not_found", "artifact %s 不存在", id)
	}
	if err != nil {
		return nil, err
	}
	if a.User == nil || (!a.User.IsAdmin() && a.User.ID != art.OwnerID) {
		// 不暴露他人 artifact 是否存在
		return nil, errf(404, "not_found", "artifact %s 不存在", id)
	}
	if write && art.DeletedAt != nil {
		return nil, errf(410, "deleted", "artifact %s 已在回收站，先恢复再修改", id)
	}
	return art, nil
}

func (s *Service) freeDiskMB() int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.Cfg.DataDir, &st); err != nil {
		return 1 << 30
	}
	return int64(st.Bavail) * int64(st.Bsize) / (1 << 20)
}

// Publish 新建 artifact 或为已有 artifact 生成新版本。
func (s *Service) Publish(ctx context.Context, in PublishInput) (*PublishResult, error) {
	if in.Actor.User == nil {
		return nil, errf(401, "unauthorized", "未登录")
	}
	st := s.Store.Settings()
	var art *store.Artifact
	cur := []store.VersionFile{}
	owner := in.Actor.User
	if in.ArtifactID != "" {
		var err error
		art, err = s.Load(in.Actor, in.ArtifactID, true)
		if err != nil {
			return nil, err
		}
		if in.BaseVersion != nil && *in.BaseVersion != art.CurrentVersion {
			e := errf(409, "conflict", "版本冲突：你基于 v%d 修改，但服务端当前是 v%d。先拉取最新版本合并后再发布。", *in.BaseVersion, art.CurrentVersion)
			e.Data = map[string]any{"current_version": art.CurrentVersion}
			return nil, e
		}
		if cur, err = s.Store.VersionFiles(art.ID, art.CurrentVersion); err != nil {
			return nil, err
		}
		if art.OwnerID != owner.ID {
			if owner, err = s.Store.UserByID(art.OwnerID); err != nil {
				return nil, err
			}
		}
	}
	q := owner.Quota(st)
	if art == nil && s.Store.CountArtifacts(owner.ID) >= q.Artifacts {
		return nil, errf(403, "quota_artifacts", "artifact 数量已达上限（%d 个）。删除不用的 artifact，或请管理员提高限额。", q.Artifacts)
	}

	// 1. 组装新版本的文件清单
	manifest := map[string]store.VersionFile{}
	if !in.Replace {
		for _, f := range cur {
			manifest[f.Path] = f
		}
	}
	newBlobs := map[string][]byte{}
	for p, fi := range in.Files {
		cp, err := CleanPath(p)
		if err != nil {
			return nil, errf(400, "bad_path", "%s", err.Error())
		}
		if fi == nil {
			if _, ok := manifest[cp]; !ok && !in.Replace {
				return nil, errf(400, "bad_path", "要删除的文件 %q 不存在", cp)
			}
			delete(manifest, cp)
			continue
		}
		if fi.Ref != nil {
			ref := *fi.Ref
			ref.Path = cp
			manifest[cp] = ref
			continue
		}
		if int64(len(fi.Content)) > q.FileBytes {
			return nil, errf(413, "file_too_large", "文件 %s 大小 %s，超过单文件上限 %s", cp, auth.FormatBytes(int64(len(fi.Content))), auth.FormatBytes(q.FileBytes))
		}
		mime, err := DetectMime(cp, fi.Content)
		if err != nil {
			return nil, errf(415, "bad_type", "%s", err.Error())
		}
		sha := blob.Sum(fi.Content)
		manifest[cp] = store.VersionFile{Path: cp, SHA256: sha, Size: int64(len(fi.Content)), Mime: mime}
		newBlobs[sha] = fi.Content
	}
	if len(manifest) == 0 {
		return nil, errf(400, "empty", "没有任何文件可发布")
	}
	if int64(len(manifest)) > q.Files {
		return nil, errf(413, "too_many_files", "单个版本最多 %d 个文件，本次有 %d 个", q.Files, len(manifest))
	}
	var total int64
	for _, f := range manifest {
		total += f.Size
	}
	if total > q.VersionBytes {
		return nil, errf(413, "version_too_large", "本版本总大小 %s，超过上限 %s", auth.FormatBytes(total), auth.FormatBytes(q.VersionBytes))
	}

	// 2. 入口文件与类型
	entry := ""
	if in.Entry != nil && strings.TrimSpace(*in.Entry) != "" {
		e, err := CleanPath(*in.Entry)
		if err != nil {
			return nil, errf(400, "bad_entry", "%s", err.Error())
		}
		entry = e
	} else if art != nil {
		if _, ok := manifest[art.Entry]; ok {
			entry = art.Entry
		}
	}
	if entry == "" {
		entry = guessEntry(manifest)
	}
	if entry == "" {
		return nil, errf(400, "no_entry", "找不到入口文件。请提供 index.html（或唯一的 .html/.md 文件），或用 entry 指定。")
	}
	if _, ok := manifest[entry]; !ok {
		return nil, errf(400, "no_entry", "入口文件 %s 不在本次文件列表中", entry)
	}
	kind, err := KindOf(entry)
	if err != nil {
		return nil, errf(400, "bad_entry", "%s", err.Error())
	}

	// 3. 存储配额（同一用户内按内容去重计算）
	owned, err := s.Store.UserBlobSet(owner.ID)
	if err != nil {
		return nil, err
	}
	var added int64
	counted := map[string]bool{}
	for _, f := range manifest {
		if !owned[f.SHA256] && !counted[f.SHA256] {
			added += f.Size
			counted[f.SHA256] = true
		}
	}
	if added > 0 {
		used, err := s.Store.UserStorage(owner.ID)
		if err != nil {
			return nil, err
		}
		if used+added > q.StorageBytes {
			return nil, errf(413, "quota_storage", "存储空间不足：已用 %s，本次新增 %s，上限 %s。清理回收站或旧 artifact 后重试。", auth.FormatBytes(used), auth.FormatBytes(added), auth.FormatBytes(q.StorageBytes))
		}
		if s.freeDiskMB() < s.Cfg.MinFreeDiskMB {
			return nil, errf(507, "disk_full", "服务器磁盘剩余空间不足，暂停写入。请联系管理员。")
		}
	}

	// 4. 文本内容（用于检查、标题、检索）
	texts := map[string][]byte{}
	allPaths := map[string]bool{}
	for p, f := range manifest {
		allPaths[p] = true
		ext := strings.ToLower(path.Ext(p))
		if (ext == ".html" || ext == ".htm" || ext == ".md" || ext == ".markdown" || (ext == ".svg" && p == entry)) && f.Size <= 3<<20 {
			if b, ok := newBlobs[f.SHA256]; ok {
				texts[p] = b
			} else if b, err := s.Blobs.Read(f.SHA256); err == nil {
				texts[p] = b
			}
		}
	}
	warnings := lint.Check(lint.Input{Files: texts, AllPaths: allPaths, Entry: entry, Allowlist: st.CDNAllowlist, LibExists: s.LibExists})

	// 5. 元数据
	title := ""
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	if title == "" {
		title = extractTitle(kind, texts[entry])
	}
	if title == "" && art != nil {
		title = art.Title
	}
	if title == "" {
		title = strings.TrimSuffix(path.Base(entry), path.Ext(entry))
	}
	title = truncate(title, 120)
	desc := ""
	if art != nil {
		desc = art.Description
	}
	if in.Description != nil {
		desc = truncate(strings.TrimSpace(*in.Description), 500)
	} else if art == nil {
		desc = truncate(extractDescription(kind, texts[entry]), 500)
	}
	vis := st.DefaultVisibility
	if art != nil {
		vis = art.Visibility
	}
	if in.Visibility != nil && *in.Visibility != "" {
		if !validVisibility(*in.Visibility) {
			return nil, errf(400, "bad_visibility", "可见性必须是 private、unlisted 或 public")
		}
		vis = *in.Visibility
	}

	files := make([]store.VersionFile, 0, len(manifest))
	for _, f := range manifest {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	res := &PublishResult{Title: title, Visibility: vis, Entry: entry, Kind: kind, Files: len(files), Size: total, Warnings: warnings}
	if res.Warnings == nil {
		res.Warnings = []lint.Warning{}
	}

	// 内容与入口都没变：只更新元数据，不生成新版本
	if art != nil && sameFiles(cur, files) && entry == art.Entry {
		if title != art.Title || desc != art.Description || vis != art.Visibility {
			if err := s.Store.UpdateMeta(art.ID, &title, &desc, &vis, nil); err != nil {
				return nil, err
			}
			s.Store.Audit(in.Actor.uid(), in.Actor.KeyID, in.Actor.IP, "artifact.meta", art.ID, "")
		}
		res.ID, res.Version, res.Unchanged = art.ID, art.CurrentVersion, true
		res.URL, res.RawURL = s.ViewURL(art.ID), s.rawURL(art.ID)
		return res, nil
	}

	// 6. 写入 blob 与数据库
	s.gcMu.RLock()
	defer s.gcMu.RUnlock()
	for _, data := range newBlobs {
		if _, err := s.Blobs.Put(data); err != nil {
			return nil, fmt.Errorf("写入文件失败：%w", err)
		}
	}
	for _, f := range files {
		if !s.Blobs.Has(f.SHA256) {
			return nil, errf(500, "blob_missing", "文件 %s 的内容在存储中缺失", f.Path)
		}
	}
	errs, _, _ := lint.Summary(warnings)
	nowTS := time.Now().Unix()
	body := searchText(kind, texts[entry])
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if art == nil {
			art = &store.Artifact{OwnerID: owner.ID, CreatedAt: nowTS}
			for i := 0; i < 5; i++ {
				art.ID = auth.RandomString(10)
				if !s.Store.ArtifactExists(art.ID) {
					break
				}
			}
			res.Created = true
			art.Title, art.Description, art.Kind, art.Entry, art.Visibility = title, desc, kind, entry, vis
			art.UpdatedAt = nowTS
			if err := store.InsertArtifact(tx, art); err != nil {
				return err
			}
		}
		prev := art.CurrentVersion
		v := &store.Version{ArtifactID: art.ID, Number: prev + 1, Message: truncate(in.Message, 500), Source: in.Source,
			Entry: entry, AuthorUserID: in.Actor.uid(), AuthorKeyID: in.Actor.KeyID, TotalSize: total,
			FileCount: len(files), Warnings: errs, CreatedAt: nowTS}
		if v.Source == "" {
			v.Source = "publish"
		}
		if err := store.InsertVersion(tx, v, files); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY") {
				return errf(409, "conflict", "版本冲突：有另一个发布同时写入了这个 artifact，请重试。")
			}
			return err
		}
		r, err := tx.Exec(`UPDATE artifacts SET title=?,description=?,kind=?,entry=?,visibility=?,current_version=?,size_bytes=?,updated_at=? WHERE id=? AND current_version=?`,
			title, desc, kind, entry, vis, v.Number, total, nowTS, art.ID, prev)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return errf(409, "conflict", "版本冲突：有另一个发布同时写入了这个 artifact，请重试。")
		}
		art.CurrentVersion = v.Number
		if err := store.UpsertFTS(tx, art.ID, title, desc, body); err != nil {
			return err
		}
		return store.PruneVersions(tx, art.ID, st.MaxVersions)
	})
	if err != nil {
		return nil, err
	}
	action := "artifact.publish"
	if res.Created {
		action = "artifact.create"
	} else if in.Source == "edit" {
		action = "artifact.edit"
	} else if in.Source == "restore" {
		action = "artifact.restore"
	}
	s.Store.Audit(in.Actor.uid(), in.Actor.KeyID, in.Actor.IP, action, art.ID, fmt.Sprintf("v%d %d 个文件 %s", art.CurrentVersion, len(files), auth.FormatBytes(total)))
	res.ID, res.Version = art.ID, art.CurrentVersion
	res.URL, res.RawURL = s.ViewURL(art.ID), s.rawURL(art.ID)
	return res, nil
}

func (s *Service) rawURL(id string) string { return s.Cfg.PublicURL + "/a/" + id + "/raw" }

func guessEntry(m map[string]store.VersionFile) string {
	for _, c := range []string{"index.html", "index.htm", "index.md", "README.md", "index.svg"} {
		if _, ok := m[c]; ok {
			return c
		}
	}
	var htmls, mds, svgs []string
	for p := range m {
		switch strings.ToLower(path.Ext(p)) {
		case ".html", ".htm":
			htmls = append(htmls, p)
		case ".md", ".markdown":
			mds = append(mds, p)
		case ".svg":
			svgs = append(svgs, p)
		}
	}
	switch {
	case len(htmls) == 1:
		return htmls[0]
	case len(htmls) == 0 && len(mds) == 1:
		return mds[0]
	case len(htmls) == 0 && len(mds) == 0 && len(svgs) == 1:
		return svgs[0]
	}
	return ""
}

func sameFiles(a, b []store.VersionFile) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]string{}
	for _, f := range a {
		m[f.Path] = f.SHA256
	}
	for _, f := range b {
		if m[f.Path] != f.SHA256 {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// ---- 局部编辑 ----

type Edit struct {
	Path   string `json:"path"`
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

// ApplyEdits 依次执行字符串替换。每个 old_str 必须在当前内容中恰好出现一次。
func ApplyEdits(content string, edits []Edit) (string, error) {
	for i, e := range edits {
		if e.OldStr == "" {
			return "", errf(422, "edit_failed", "第 %d 处编辑的 old_str 为空", i+1)
		}
		n := strings.Count(content, e.OldStr)
		if n == 0 {
			return "", &Error{Status: 422, Code: "edit_failed", Msg: fmt.Sprintf("第 %d 处编辑没有找到 old_str（需逐字一致，包括空格和换行）", i+1), Data: map[string]any{"index": i}}
		}
		if n > 1 {
			return "", &Error{Status: 422, Code: "edit_failed", Msg: fmt.Sprintf("第 %d 处编辑的 old_str 出现了 %d 次，请多带一些上下文让它唯一", i+1, n), Data: map[string]any{"index": i}}
		}
		content = strings.Replace(content, e.OldStr, e.NewStr, 1)
	}
	return content, nil
}

func (s *Service) Edit(ctx context.Context, a Actor, id string, base *int, edits []Edit, message string) (*PublishResult, error) {
	art, err := s.Load(a, id, true)
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, errf(400, "empty", "没有编辑内容")
	}
	if base != nil && *base != art.CurrentVersion {
		e := errf(409, "conflict", "版本冲突：你基于 v%d 修改，但服务端当前是 v%d。", *base, art.CurrentVersion)
		e.Data = map[string]any{"current_version": art.CurrentVersion}
		return nil, e
	}
	byPath := map[string][]Edit{}
	order := []string{}
	for _, e := range edits {
		p := e.Path
		if p == "" {
			p = art.Entry
		}
		cp, err := CleanPath(p)
		if err != nil {
			return nil, errf(400, "bad_path", "%s", err.Error())
		}
		if _, ok := byPath[cp]; !ok {
			order = append(order, cp)
		}
		byPath[cp] = append(byPath[cp], e)
	}
	files := map[string]*FileInput{}
	for _, p := range order {
		vf, err := s.Store.VersionFile(art.ID, art.CurrentVersion, p)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errf(404, "not_found", "当前版本里没有文件 %s", p)
		} else if err != nil {
			return nil, err
		}
		if !IsText(vf.Mime) {
			return nil, errf(400, "not_text", "文件 %s 不是文本，不能局部编辑", p)
		}
		b, err := s.Blobs.Read(vf.SHA256)
		if err != nil {
			return nil, err
		}
		out, err := ApplyEdits(string(b), byPath[p])
		if err != nil {
			var ae *Error
			if errors.As(err, &ae) {
				ae.Msg = p + "：" + ae.Msg
			}
			return nil, err
		}
		files[p] = &FileInput{Content: []byte(out)}
	}
	cv := art.CurrentVersion
	return s.Publish(ctx, PublishInput{ArtifactID: art.ID, BaseVersion: &cv, Files: files, Message: message, Source: "edit", Actor: a})
}

// Restore 用旧版本的内容生成一个新版本，历史记录保持不变。
func (s *Service) Restore(ctx context.Context, a Actor, id string, version int) (*PublishResult, error) {
	art, err := s.Load(a, id, true)
	if err != nil {
		return nil, err
	}
	v, err := s.Store.VersionByNumber(art.ID, version)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(404, "not_found", "版本 v%d 不存在（可能已按保留策略清理）", version)
	} else if err != nil {
		return nil, err
	}
	vfs, err := s.Store.VersionFiles(art.ID, v.Number)
	if err != nil {
		return nil, err
	}
	files := map[string]*FileInput{}
	for i := range vfs {
		f := vfs[i]
		files[f.Path] = &FileInput{Ref: &f}
	}
	entry := v.Entry
	cv := art.CurrentVersion
	return s.Publish(ctx, PublishInput{ArtifactID: art.ID, BaseVersion: &cv, Files: files, Replace: true, Entry: &entry,
		Message: fmt.Sprintf("恢复到 v%d", version), Source: "restore", Actor: a})
}

func (s *Service) Delete(a Actor, id string) error {
	art, err := s.Load(a, id, false)
	if err != nil {
		return err
	}
	if art.DeletedAt != nil {
		return nil
	}
	if err := s.Store.SoftDelete(id); err != nil {
		return err
	}
	s.Store.Audit(a.uid(), a.KeyID, a.IP, "artifact.delete", id, art.Title)
	return nil
}

func (s *Service) Undelete(a Actor, id string) error {
	art, err := s.Load(a, id, false)
	if err != nil {
		return err
	}
	if art.DeletedAt == nil {
		return nil
	}
	if s.Store.CountArtifacts(art.OwnerID) >= func() int64 {
		o, err := s.Store.UserByID(art.OwnerID)
		if err != nil {
			return 0
		}
		return o.Quota(s.Store.Settings()).Artifacts
	}() {
		return errf(403, "quota_artifacts", "所有者的 artifact 数量已达上限，无法恢复")
	}
	if err := s.Store.Undelete(id); err != nil {
		return err
	}
	s.Store.Audit(a.uid(), a.KeyID, a.IP, "artifact.undelete", id, art.Title)
	return nil
}

func (s *Service) Purge(a Actor, id string) error {
	art, err := s.Load(a, id, false)
	if err != nil {
		return err
	}
	if art.DeletedAt == nil {
		return errf(400, "not_deleted", "只能彻底删除回收站里的 artifact，请先删除")
	}
	if err := s.Store.Purge(id); err != nil {
		return err
	}
	s.Store.Audit(a.uid(), a.KeyID, a.IP, "artifact.purge", id, art.Title)
	return nil
}

type MetaPatch struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Visibility  *string `json:"visibility"`
	OwnerID     *int64  `json:"owner_id"`
}

func (s *Service) UpdateMeta(a Actor, id string, p MetaPatch) (*store.Artifact, error) {
	art, err := s.Load(a, id, true)
	if err != nil {
		return nil, err
	}
	if p.Visibility != nil && !validVisibility(*p.Visibility) {
		return nil, errf(400, "bad_visibility", "可见性必须是 private、unlisted 或 public")
	}
	if p.OwnerID != nil {
		if !a.User.IsAdmin() {
			return nil, errf(403, "forbidden", "只有管理员可以转移所有者")
		}
		if _, err := s.Store.UserByID(*p.OwnerID); err != nil {
			return nil, errf(400, "bad_owner", "目标用户不存在")
		}
	}
	if p.Title != nil {
		t := truncate(strings.TrimSpace(*p.Title), 120)
		p.Title = &t
	}
	if p.Description != nil {
		d := truncate(strings.TrimSpace(*p.Description), 500)
		p.Description = &d
	}
	if err := s.Store.UpdateMeta(art.ID, p.Title, p.Description, p.Visibility, p.OwnerID); err != nil {
		return nil, err
	}
	detail := []string{}
	if p.Visibility != nil && *p.Visibility != art.Visibility {
		detail = append(detail, "可见性 "+art.Visibility+" → "+*p.Visibility)
	}
	if p.OwnerID != nil && *p.OwnerID != art.OwnerID {
		detail = append(detail, fmt.Sprintf("所有者 %d → %d", art.OwnerID, *p.OwnerID))
	}
	if p.Title != nil && *p.Title != art.Title {
		detail = append(detail, "标题")
	}
	s.Store.Audit(a.uid(), a.KeyID, a.IP, "artifact.meta", art.ID, strings.Join(detail, "；"))
	return s.Store.ArtifactByID(art.ID)
}

// GC 删除没有任何版本引用的 blob，并清理过期回收站。
func (s *Service) GC() (int, int64, error) {
	st := s.Store.Settings()
	for _, id := range s.Store.ExpiredTrash(st.TrashDays) {
		if err := s.Store.Purge(id); err == nil {
			s.Store.Audit(nil, nil, "", "artifact.purge", id, "回收站到期自动清理")
		}
	}
	s.gcMu.Lock()
	defer s.gcMu.Unlock()
	blobs, err := s.Store.UnreferencedBlobs()
	if err != nil {
		return 0, 0, err
	}
	var freed int64
	n := 0
	for _, b := range blobs {
		if err := s.Blobs.Delete(b.SHA256); err != nil {
			continue
		}
		s.Store.DeleteBlobRow(b.SHA256)
		freed += b.Size
		n++
	}
	return n, freed, nil
}
