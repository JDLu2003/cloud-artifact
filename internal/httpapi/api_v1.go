package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"vps-artifact/internal/artifact"
	"vps-artifact/internal/lint"
	"vps-artifact/internal/store"
)

type publishReq struct {
	Title       *string                    `json:"title"`
	Description *string                    `json:"description"`
	Visibility  *string                    `json:"visibility"`
	Entry       *string                    `json:"entry"`
	Message     string                     `json:"message"`
	BaseVersion *int                       `json:"base_version"`
	Mode        string                     `json:"mode"` // merge（默认）| replace
	Files       map[string]json.RawMessage `json:"files"`
}

type fileObj struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// parsePublish 解析 JSON 或 multipart 请求体，得到元数据和文件。
func parsePublish(r *http.Request) (*publishReq, map[string]*artifact.FileInput, error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	req := &publishReq{}
	files := map[string]*artifact.FileInput{}
	if ct == "multipart/form-data" {
		mr, err := r.MultipartReader()
		if err != nil {
			return nil, nil, badRequest("multipart 解析失败：" + err.Error())
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, badRequest("multipart 解析失败：" + err.Error())
			}
			name := part.FormName()
			data, err := io.ReadAll(part)
			if err != nil {
				return nil, nil, err
			}
			switch {
			case name == "meta":
				if err := json.Unmarshal(data, req); err != nil {
					return nil, nil, badRequest("meta 不是合法的 JSON：" + err.Error())
				}
			case strings.HasPrefix(name, "file:"):
				files[strings.TrimPrefix(name, "file:")] = &artifact.FileInput{Content: data}
			case name == "file" && part.FileName() != "":
				files[part.FileName()] = &artifact.FileInput{Content: data}
			case strings.HasPrefix(name, "delete:"):
				files[strings.TrimPrefix(name, "delete:")] = nil
			}
		}
	} else {
		if err := decodeJSON(r, req); err != nil {
			return nil, nil, err
		}
	}
	for p, raw := range req.Files {
		t := strings.TrimSpace(string(raw))
		switch {
		case t == "null":
			files[p] = nil
		case strings.HasPrefix(t, `"`):
			var sv string
			if err := json.Unmarshal(raw, &sv); err != nil {
				return nil, nil, badRequest(fmt.Sprintf("文件 %s 的内容无法解析", p))
			}
			files[p] = &artifact.FileInput{Content: []byte(sv)}
		case strings.HasPrefix(t, "{"):
			var fo fileObj
			if err := json.Unmarshal(raw, &fo); err != nil {
				return nil, nil, badRequest(fmt.Sprintf("文件 %s 的内容无法解析", p))
			}
			switch strings.ToLower(fo.Encoding) {
			case "", "utf-8", "utf8", "text":
				files[p] = &artifact.FileInput{Content: []byte(fo.Content)}
			case "base64":
				b, err := base64.StdEncoding.DecodeString(fo.Content)
				if err != nil {
					return nil, nil, badRequest(fmt.Sprintf("文件 %s 的 base64 内容无效", p))
				}
				files[p] = &artifact.FileInput{Content: b}
			default:
				return nil, nil, badRequest(fmt.Sprintf("文件 %s 的 encoding 只能是 utf-8 或 base64", p))
			}
		default:
			return nil, nil, badRequest(fmt.Sprintf("文件 %s 的值必须是字符串、对象或 null", p))
		}
	}
	return req, files, nil
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	st := s.Store.Settings()
	used, _ := s.Store.UserStorage(a.User.ID)
	writeJSON(w, 200, map[string]any{
		"user":               map[string]any{"id": a.User.ID, "name": a.User.Name, "display_name": a.User.DisplayName, "role": a.User.Role},
		"quota":              a.User.Quota(st),
		"usage":              map[string]any{"storage_bytes": used, "artifacts": s.Store.CountArtifacts(a.User.ID)},
		"server":             s.Cfg.PublicURL,
		"server_version":     s.Version,
		"skill_version":      s.Skill.Version,
		"default_visibility": st.DefaultVisibility,
		"cdn_allowlist":      st.CDNAllowlist,
	})
}

func (s *Server) apiLibs(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var files []string
	fs.WalkDir(s.LibFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.Contains(p, "LICENSE") && !strings.Contains(p, "/fonts/") {
			files = append(files, "/_lib/"+p)
		}
		return nil
	})
	sort.Strings(files)
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	req, files, err := parsePublish(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	texts := map[string][]byte{}
	all := map[string]bool{}
	for p, f := range files {
		cp, err := artifact.CleanPath(p)
		if err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		if f == nil {
			continue
		}
		all[cp] = true
		texts[cp] = f.Content
	}
	entry := ""
	if req.Entry != nil {
		entry = *req.Entry
	}
	if entry == "" {
		for _, c := range []string{"index.html", "index.md", "README.md"} {
			if all[c] {
				entry = c
				break
			}
		}
		if entry == "" {
			for p := range all {
				if strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".md") {
					entry = p
					break
				}
			}
		}
	}
	ws := lint.Check(lint.Input{Files: texts, AllPaths: all, Entry: entry, Allowlist: s.Store.Settings().CDNAllowlist, LibExists: LibExists(s.LibFS)})
	if ws == nil {
		ws = []lint.Warning{}
	}
	e, wn, i := lint.Summary(ws)
	writeJSON(w, 200, map[string]any{"entry": entry, "warnings": ws, "summary": map[string]int{"error": e, "warn": wn, "info": i}})
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	req, files, err := parsePublish(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.allowPublish(w, a) {
		return
	}
	res, err := s.Svc.Publish(r.Context(), artifact.PublishInput{
		Files: files, Replace: true, Title: req.Title, Description: req.Description, Visibility: req.Visibility,
		Entry: req.Entry, Message: req.Message, Source: "publish", Actor: a,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) allowPublish(w http.ResponseWriter, a artifact.Actor) bool {
	q := a.User.Quota(s.Store.Settings())
	if ok, wait := s.Limiter.Allow(fmt.Sprintf("publish:%d", a.User.ID), q.PublishPerMin); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeErr(w, &artifact.Error{Status: 429, Code: "rate_limited", Msg: fmt.Sprintf("发布过于频繁（每分钟上限 %d 次），请 %d 秒后再试", q.PublishPerMin, int(wait.Seconds())+1)})
		return false
	}
	return true
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	req, files, err := parsePublish(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.allowPublish(w, a) {
		return
	}
	mode := strings.ToLower(req.Mode)
	if mode != "" && mode != "merge" && mode != "replace" {
		writeErr(w, badRequest("mode 只能是 merge 或 replace"))
		return
	}
	res, err := s.Svc.Publish(r.Context(), artifact.PublishInput{
		ArtifactID: r.PathValue("id"), BaseVersion: req.BaseVersion, Files: files, Replace: mode == "replace",
		Title: req.Title, Description: req.Description, Visibility: req.Visibility, Entry: req.Entry,
		Message: req.Message, Source: "publish", Actor: a,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) apiPatch(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var p artifact.MetaPatch
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	p.OwnerID = nil // 转移所有者只能在后台操作
	art, err := s.Svc.UpdateMeta(a, r.PathValue("id"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, s.artifactJSON(art))
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	if err := s.Svc.Delete(a, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "已移入回收站，可在管理后台恢复"})
}

func (s *Server) apiEdits(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var req struct {
		BaseVersion *int            `json:"base_version"`
		Message     string          `json:"message"`
		Edits       []artifact.Edit `json:"edits"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if !s.allowPublish(w, a) {
		return
	}
	res, err := s.Svc.Edit(r.Context(), a, r.PathValue("id"), req.BaseVersion, req.Edits, req.Message)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) apiRestore(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var req struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.Svc.Restore(r.Context(), a, r.PathValue("id"), req.Version)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) artifactJSON(a *store.Artifact) map[string]any {
	return map[string]any{
		"id": a.ID, "title": a.Title, "description": a.Description, "kind": a.Kind, "entry": a.Entry,
		"visibility": a.Visibility, "current_version": a.CurrentVersion, "views": a.Views, "size": a.SizeBytes,
		"owner": a.OwnerName, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt, "deleted": a.DeletedAt != nil,
		"url": s.Svc.ViewURL(a.ID),
	}
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	res, err := s.Store.ListArtifacts(store.ListQuery{OwnerID: a.User.ID, Query: q.Get("q"), Limit: limit, Offset: offset, Deleted: q.Get("deleted") == "1"})
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]map[string]any, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, s.artifactJSON(&res.Items[i]))
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": res.Total})
}

func (s *Server) apiGet(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	files, _ := s.Store.VersionFiles(art.ID, art.CurrentVersion)
	vs, _ := s.Store.Versions(art.ID)
	out := s.artifactJSON(art)
	out["files"] = files
	if len(vs) > 20 {
		vs = vs[:20]
	}
	out["versions"] = vs
	writeJSON(w, 200, out)
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	vs, err := s.Store.Versions(art.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"current_version": art.CurrentVersion, "versions": vs})
}

func parseVersion(art *store.Artifact, s string) (int, error) {
	if s == "" || s == "latest" || s == "current" {
		return art.CurrentVersion, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "v"))
	if err != nil || n <= 0 {
		return 0, badRequest("版本号无效：" + s)
	}
	return n, nil
}

func (s *Server) apiVersion(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := parseVersion(art, r.PathValue("n"))
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.Store.VersionByNumber(art.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: fmt.Sprintf("版本 v%d 不存在", n)})
		return
	}
	files, _ := s.Store.VersionFiles(art.ID, n)
	writeJSON(w, 200, map[string]any{"version": v, "files": files})
}

func (s *Server) apiVersionFile(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := parseVersion(art, r.PathValue("n"))
	if err != nil {
		writeErr(w, err)
		return
	}
	vf, err := s.Store.VersionFile(art.ID, n, r.PathValue("path"))
	if err != nil {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "文件不存在"})
		return
	}
	data, err := s.Svc.Blobs.Read(vf.SHA256)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", vf.Mime)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-SHA256", vf.SHA256)
	w.Write(data)
}

func (s *Server) apiDiff(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	to, err := parseVersion(art, q.Get("to"))
	if err != nil {
		writeErr(w, err)
		return
	}
	from := to - 1
	if q.Get("from") != "" {
		if from, err = parseVersion(art, q.Get("from")); err != nil {
			writeErr(w, err)
			return
		}
	}
	if from < 1 {
		writeErr(w, badRequest("没有更早的版本可以对比"))
		return
	}
	d, err := s.Svc.DiffVersions(art.ID, from, to, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if q.Get("format") == "unified" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		var b strings.Builder
		fmt.Fprintf(&b, "v%d → v%d：%d 个文件有变化，+%d −%d 行\n\n", d.From, d.To, d.Changed, d.Added, d.Removed)
		for _, f := range d.Files {
			switch {
			case f.Status == "unchanged":
				continue
			case f.Binary:
				st := map[string]string{"added": "新增", "removed": "删除", "modified": "修改"}[f.Status]
				fmt.Fprintf(&b, "二进制文件 %s：%s（%d → %d 字节）\n\n", f.Path, st, f.OldSize, f.NewSize)
			case f.TooBig:
				fmt.Fprintf(&b, "文件 %s 过大，未逐行对比\n\n", f.Path)
			default:
				b.WriteString(f.Unified)
				b.WriteString("\n")
			}
		}
		w.Write([]byte(b.String()))
		return
	}
	writeJSON(w, 200, d)
}
