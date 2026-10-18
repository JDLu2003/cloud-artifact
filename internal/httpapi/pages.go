package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"vps-artifact/internal/artifact"
	"vps-artifact/internal/store"
)

const contentTTL = 12 * time.Hour

func (s *Server) contentURL(id string, ver int, tok, p string) string {
	if tok == "" {
		tok = "-"
	}
	esc := []string{}
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			esc = append(esc, url.PathEscape(seg))
		}
	}
	return fmt.Sprintf("%s/c/%s/v%d/%s/%s", s.Cfg.ContentBase(), id, ver, tok, strings.Join(esc, "/"))
}

// viewAccess 判断当前访问者能否看这个 artifact，并给出内容地址需要的令牌。
// 返回 manage=true 表示所有者或管理员（可看历史、源码、对比）。
func (s *Server) viewAccess(r *http.Request, art *store.Artifact) (u *store.User, manage bool, ok bool) {
	u, _ = s.sessionUser(r)
	manage = canManage(u, art)
	if art.DeletedAt != nil && !manage {
		return u, false, false
	}
	if art.Visibility == "private" && !manage {
		return u, false, false
	}
	return u, manage, true
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "模板渲染失败："+err.Error(), 500)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	frames := s.Cfg.ContentBase()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self' "+frames+"; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

type pageMsg struct {
	Title, Heading, Body, Link, LinkText string
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, msg string) {
	if msg == "" {
		msg = "这个页面不存在，或者你没有查看权限。"
	}
	link, text := "/", "回到首页"
	if u, _ := s.sessionUser(r); u == nil {
		link, text = "/admin/?next="+url.QueryEscape(r.URL.RequestURI())+"#/login", "登录后再试"
	}
	s.render(w, 404, "message.tmpl", pageMsg{Title: "找不到页面", Heading: "找不到页面", Body: msg, Link: link, LinkText: text})
}

func (s *Server) pageHome(w http.ResponseWriter, r *http.Request) {
	u, _ := s.sessionUser(r)
	pub, _ := s.Store.ListArtifacts(store.ListQuery{Visibility: "public", Limit: 12})
	s.render(w, 200, "home.tmpl", map[string]any{
		"User": u, "Server": s.Cfg.PublicURL, "Public": pub.Items, "SkillVersion": s.Skill.Version, "Version": s.Version,
	})
}

func (s *Server) loadForPage(w http.ResponseWriter, r *http.Request) (*store.Artifact, *store.User, bool, bool) {
	art, err := s.Store.ArtifactByID(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r, "")
		return nil, nil, false, false
	}
	u, manage, ok := s.viewAccess(r, art)
	if !ok {
		s.notFound(w, r, "")
		return nil, nil, false, false
	}
	return art, u, manage, true
}

func (s *Server) pageView(w http.ResponseWriter, r *http.Request) {
	art, u, manage, ok := s.loadForPage(w, r)
	if !ok {
		return
	}
	ver := art.CurrentVersion
	if v := r.URL.Query().Get("v"); v != "" && manage {
		if n, err := strconv.Atoi(strings.TrimPrefix(v, "v")); err == nil && n > 0 {
			ver = n
		}
	}
	vinfo, err := s.Store.VersionByNumber(art.ID, ver)
	if err != nil {
		s.notFound(w, r, fmt.Sprintf("版本 v%d 不存在。", ver))
		return
	}
	tok := "-"
	if manage || art.Visibility == "private" || ver != art.CurrentVersion || art.DeletedAt != nil {
		tok = s.Signer.ContentToken(art.ID, ver, contentTTL)
	}
	if !manage {
		s.Store.IncViews(art.ID)
	}
	var versions []store.Version
	if manage {
		versions, _ = s.Store.Versions(art.ID)
	}
	s.render(w, 200, "view.tmpl", map[string]any{
		"A": art, "V": vinfo, "User": u, "Manage": manage, "Versions": versions,
		"Src":    s.contentURL(art.ID, ver, tok, ""),
		"IsHead": ver == art.CurrentVersion,
		"Bare":   r.URL.Query().Has("bare"),
	})
}

func (s *Server) pageRaw(w http.ResponseWriter, r *http.Request) {
	art, _, manage, ok := s.loadForPage(w, r)
	if !ok {
		return
	}
	ver := art.CurrentVersion
	if v := r.URL.Query().Get("v"); v != "" && manage {
		if n, err := strconv.Atoi(strings.TrimPrefix(v, "v")); err == nil && n > 0 {
			ver = n
		}
	}
	tok := "-"
	if manage || art.Visibility == "private" || ver != art.CurrentVersion {
		tok = s.Signer.ContentToken(art.ID, ver, contentTTL)
	}
	if !manage {
		s.Store.IncViews(art.ID)
	}
	http.Redirect(w, r, s.contentURL(art.ID, ver, tok, ""), http.StatusFound)
}

func (s *Server) pageHistory(w http.ResponseWriter, r *http.Request) {
	art, u, manage, ok := s.loadForPage(w, r)
	if !ok {
		return
	}
	if !manage {
		s.notFound(w, r, "只有所有者和管理员可以查看历史版本。")
		return
	}
	vs, _ := s.Store.Versions(art.ID)
	_, tok := s.sessionUser(r)
	s.render(w, 200, "history.tmpl", map[string]any{"A": art, "User": u, "Versions": vs, "CSRF": s.Signer.CSRFToken(tok)})
}

func (s *Server) pageDiff(w http.ResponseWriter, r *http.Request) {
	art, u, manage, ok := s.loadForPage(w, r)
	if !ok {
		return
	}
	if !manage {
		s.notFound(w, r, "只有所有者和管理员可以对比版本。")
		return
	}
	q := r.URL.Query()
	to, err := parseVersion(art, q.Get("to"))
	if err != nil {
		to = art.CurrentVersion
	}
	from := to - 1
	if q.Get("from") != "" {
		if f, err := parseVersion(art, q.Get("from")); err == nil {
			from = f
		}
	}
	vs, _ := s.Store.Versions(art.ID)
	data := map[string]any{"A": art, "User": u, "Versions": vs, "From": from, "To": to, "Mode": q.Get("mode")}
	if from < 1 || from == to {
		data["Empty"] = "这是第一个版本，没有更早的版本可以对比。"
		if from == to {
			data["Empty"] = "请选择两个不同的版本。"
		}
		s.render(w, 200, "diff.tmpl", data)
		return
	}
	d, err := s.Svc.DiffVersions(art.ID, from, to, true)
	if err != nil {
		var ae *artifact.Error
		if errors.As(err, &ae) {
			data["Empty"] = ae.Msg
			s.render(w, 200, "diff.tmpl", data)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	data["D"] = d
	data["FromSrc"] = s.contentURL(art.ID, from, s.Signer.ContentToken(art.ID, from, contentTTL), "")
	data["ToSrc"] = s.contentURL(art.ID, to, s.Signer.ContentToken(art.ID, to, contentTTL), "")
	s.render(w, 200, "diff.tmpl", data)
}

func (s *Server) pageSource(w http.ResponseWriter, r *http.Request) {
	art, u, manage, ok := s.loadForPage(w, r)
	if !ok {
		return
	}
	if !manage {
		s.notFound(w, r, "只有所有者和管理员可以查看源码。")
		return
	}
	q := r.URL.Query()
	ver, err := parseVersion(art, q.Get("v"))
	if err != nil {
		ver = art.CurrentVersion
	}
	files, _ := s.Store.VersionFiles(art.ID, ver)
	if len(files) == 0 {
		s.notFound(w, r, fmt.Sprintf("版本 v%d 不存在。", ver))
		return
	}
	cur := q.Get("path")
	vinfo, _ := s.Store.VersionByNumber(art.ID, ver)
	if cur == "" && vinfo != nil {
		cur = vinfo.Entry
	}
	var sel *store.VersionFile
	for i := range files {
		if files[i].Path == cur {
			sel = &files[i]
		}
	}
	if sel == nil {
		sel = &files[0]
	}
	data := map[string]any{"A": art, "User": u, "Ver": ver, "Files": files, "Sel": sel}
	tok := s.Signer.ContentToken(art.ID, ver, contentTTL)
	data["FileURL"] = s.contentURL(art.ID, ver, tok, sel.Path) + "?raw=1"
	if artifact.IsText(sel.Mime) && sel.Size <= 2<<20 {
		b, err := s.Svc.Blobs.Read(sel.SHA256)
		if err == nil {
			lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			data["Lines"] = lines
			data["Lang"] = langOf(sel.Path)
		}
	}
	data["IsImage"] = strings.HasPrefix(sel.Mime, "image/")
	s.render(w, 200, "source.tmpl", data)
}

func langOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm", ".svg", ".xml":
		return "xml"
	case ".css":
		return "css"
	case ".js", ".mjs":
		return "javascript"
	case ".json", ".map", ".geojson":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	}
	return "plaintext"
}

// ---- 内容服务 ----

func (s *Server) contentCSP(r *http.Request) string {
	origins := []string{s.Cfg.ContentBase()}
	for _, o := range []string{s.Cfg.Origin(), s.requestOrigin(r)} {
		dup := false
		for _, x := range origins {
			if strings.EqualFold(x, o) {
				dup = true
			}
		}
		if !dup {
			origins = append(origins, o)
		}
	}
	self := "'self' " + strings.Join(origins, " ")
	cdn := strings.Join(s.Store.Settings().CDNAllowlist, " ")
	return strings.Join([]string{
		"sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-pointer-lock",
		"default-src " + self + " data: blob:",
		"script-src " + self + " 'unsafe-inline' 'unsafe-eval' blob: " + cdn,
		"style-src " + self + " 'unsafe-inline' " + cdn,
		"font-src " + self + " data: " + cdn,
		"img-src * data: blob:",
		"media-src * data: blob:",
		"connect-src " + self + " data: blob: " + cdn,
		"worker-src " + self + " blob:",
		"frame-src " + self + " data: blob:",
		"form-action 'none'",
		"base-uri " + self,
		"frame-ancestors " + strings.Join(origins, " "),
	}, "; ")
}

func (s *Server) serveContent(w http.ResponseWriter, r *http.Request) {
	id, verS, tok, p := r.PathValue("id"), r.PathValue("ver"), r.PathValue("tok"), r.PathValue("path")
	ver, err := strconv.Atoi(strings.TrimPrefix(verS, "v"))
	if err != nil || !strings.HasPrefix(verS, "v") {
		http.NotFound(w, r)
		return
	}
	art, err := s.Store.ArtifactByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	signed := tok != "-" && s.Signer.VerifyContentToken(tok, id, ver)
	if !signed {
		if art.DeletedAt != nil || art.Visibility == "private" || ver != art.CurrentVersion {
			// 旧链接指向的版本已更新时，把读者带到查看页
			if art.DeletedAt == nil && art.Visibility != "private" && p == "" {
				http.Redirect(w, r, s.Svc.ViewURL(id), http.StatusFound)
				return
			}
			http.Error(w, "链接已失效或没有权限。请从 artifact 的查看页重新打开。", http.StatusForbidden)
			return
		}
	}
	vinfo, err := s.Store.VersionByNumber(id, ver)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if p == "" {
		p = vinfo.Entry
	}
	p = strings.TrimSuffix(p, "/")
	vf, err := s.Store.VersionFile(id, ver, p)
	if errors.Is(err, store.ErrNotFound) {
		vf, err = s.Store.VersionFile(id, ver, path.Join(p, "index.html"))
	}
	if err != nil {
		s.contentHeaders(w, r, signed)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(404)
		fmt.Fprintf(w, "包里没有文件：%s", p)
		return
	}
	etag := `"` + vf.SHA256[:20] + `"`
	s.contentHeaders(w, r, signed)
	raw := r.URL.Query().Has("raw")
	ext := strings.ToLower(path.Ext(vf.Path))
	if !raw && (ext == ".md" || ext == ".markdown") {
		s.serveMarkdown(w, r, art, vf)
		return
	}
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := s.Svc.Blobs.Read(vf.SHA256)
	if err != nil {
		http.Error(w, "读取内容失败", 500)
		return
	}
	ct := vf.Mime
	if raw && artifact.IsText(vf.Mime) {
		ct = "text/plain; charset=utf-8"
	} else if ext == ".html" || ext == ".htm" {
		data = artifact.WrapHTML(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (s *Server) contentHeaders(w http.ResponseWriter, r *http.Request, signed bool) {
	h := w.Header()
	h.Set("Content-Security-Policy", s.contentCSP(r))
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Access-Control-Allow-Origin", "*")
	if signed {
		h.Set("Cache-Control", "private, max-age=600")
	} else {
		h.Set("Cache-Control", "public, max-age=60")
	}
}

type mdPage struct {
	Title      string
	Body       template.HTML
	TOC        []artifact.TOCItem
	HasMermaid bool
	HasMath    bool
	HasCode    bool
	Updated    string
	Owner      string
}

func (s *Server) serveMarkdown(w http.ResponseWriter, r *http.Request, art *store.Artifact, vf *store.VersionFile) {
	data, err := s.Svc.Blobs.Read(vf.SHA256)
	if err != nil {
		http.Error(w, "读取内容失败", 500)
		return
	}
	doc, err := artifact.RenderMarkdown(data)
	if err != nil {
		http.Error(w, "Markdown 渲染失败："+err.Error(), 500)
		return
	}
	title := doc.Title
	if title == "" {
		title = art.Title
	}
	page := mdPage{Title: title, Body: template.HTML(doc.Body), TOC: doc.TOC, HasMermaid: doc.HasMermaid, HasMath: doc.HasMath, HasCode: doc.HasCode,
		Updated: time.Unix(art.UpdatedAt, 0).In(cst).Format("2006 年 1 月 2 日"), Owner: art.OwnerName}
	if len(page.TOC) < 3 {
		page.TOC = nil
	}
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, "markdown.tmpl", page); err != nil {
		http.Error(w, "模板渲染失败："+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// ---- 管理后台静态文件 ----

func (s *Server) adminStatic() http.Handler {
	fsrv := http.FileServer(http.FS(s.AdminFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-src 'self' "+s.Cfg.ContentBase()+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.URL.Path == "" || r.URL.Path == "/" || r.URL.Path == "index.html" {
			b, err := fs.ReadFile(s.AdminFS, "index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Cache-Control", "no-cache")
			h.Set("Content-Type", "text/html; charset=utf-8")
			w.Write(bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(s.Version)))
			return
		}
		h.Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}

// ---- 技能包 ----

func (s *Server) skillTarball(w http.ResponseWriter, r *http.Request) {
	b, err := s.Skill.Tarball(s.Cfg.PublicURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="artifact-skill.tar.gz"`)
	w.Header().Set("X-Skill-Version", s.Skill.Version)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Server) skillVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"version": s.Skill.Version, "files": s.Skill.Files()})
}

func (s *Server) skillFile(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	b, ok := s.Skill.File(p, s.Cfg.PublicURL)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ct := "text/plain; charset=utf-8"
	if strings.HasSuffix(p, ".html") {
		ct = "text/plain; charset=utf-8" // 模板以源码形式提供，不在本站渲染
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}
