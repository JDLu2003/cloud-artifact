// Package httpapi 提供全部 HTTP 接口：agent API、管理 API、查看页、内容服务和技能包下载。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"vps-artifact/internal/artifact"
	"vps-artifact/internal/auth"
	"vps-artifact/internal/config"
	"vps-artifact/internal/metrics"
	"vps-artifact/internal/skillpack"
	"vps-artifact/internal/store"
)

type Server struct {
	Cfg     *config.Config
	Store   *store.Store
	Svc     *artifact.Service
	Signer  *auth.Signer
	Limiter *auth.Limiter
	Metrics *metrics.Collector
	Skill   *skillpack.Pack
	Version string

	LibFS    fs.FS // web/lib
	AdminFS  fs.FS // web/admin
	ViewerFS fs.FS // web/viewer

	tpl *template.Template
	mux *http.ServeMux
}

func (s *Server) Init() error {
	funcs := template.FuncMap{
		"bytes": auth.FormatBytes,
		"time":  func(ts int64) string { return time.Unix(ts, 0).In(cst).Format("2006-01-02 15:04") },
		"ago":   ago,
		"add":   func(a, b int) int { return a + b },
		"sub":   func(a, b int) int { return a - b },
		"sourceLabel": func(s string) string {
			switch s {
			case "edit":
				return "局部编辑"
			case "restore":
				return "恢复"
			}
			return "发布"
		},
		"visLabel": visLabel,
	}
	t, err := template.New("").Funcs(funcs).ParseFS(s.ViewerFS, "*.tmpl")
	if err != nil {
		return err
	}
	s.tpl = t
	s.routes()
	return nil
}

var cst = time.FixedZone("CST", 8*3600)

func visLabel(v string) string {
	switch v {
	case "private":
		return "私有"
	case "public":
		return "公开"
	}
	return "持链接可看"
}

func ago(ts int64) string {
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
	return time.Unix(ts, 0).In(cst).Format("2006-01-02")
}

func (s *Server) Handler() http.Handler { return s.middleware(s.mux) }

func (s *Server) routes() {
	m := http.NewServeMux()
	s.mux = m

	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})

	// agent API
	m.HandleFunc("GET /api/v1/me", s.withKey(s.apiMe))
	m.HandleFunc("GET /api/v1/libs", s.withKey(s.apiLibs))
	m.HandleFunc("POST /api/v1/check", s.withKey(s.apiCheck))
	m.HandleFunc("GET /api/v1/artifacts", s.withKey(s.apiList))
	m.HandleFunc("POST /api/v1/artifacts", s.withKey(s.apiCreate))
	m.HandleFunc("GET /api/v1/artifacts/{id}", s.withKey(s.apiGet))
	m.HandleFunc("PUT /api/v1/artifacts/{id}", s.withKey(s.apiUpdate))
	m.HandleFunc("PATCH /api/v1/artifacts/{id}", s.withKey(s.apiPatch))
	m.HandleFunc("DELETE /api/v1/artifacts/{id}", s.withKey(s.apiDelete))
	m.HandleFunc("POST /api/v1/artifacts/{id}/edits", s.withKey(s.apiEdits))
	m.HandleFunc("POST /api/v1/artifacts/{id}/restore", s.withKey(s.apiRestore))
	m.HandleFunc("GET /api/v1/artifacts/{id}/versions", s.withKey(s.apiVersions))
	m.HandleFunc("GET /api/v1/artifacts/{id}/versions/{n}", s.withKey(s.apiVersion))
	m.HandleFunc("GET /api/v1/artifacts/{id}/versions/{n}/files/{path...}", s.withKey(s.apiVersionFile))
	m.HandleFunc("GET /api/v1/artifacts/{id}/diff", s.withKey(s.apiDiff))
	m.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "没有这个接口：" + r.Method + " " + r.URL.Path})
	})

	// 管理 API
	s.adminRoutes(m)

	// 页面与内容
	m.HandleFunc("GET /{$}", s.pageHome)
	m.HandleFunc("GET /a/{id}", s.pageView)
	m.HandleFunc("GET /a/{id}/raw", s.pageRaw)
	m.HandleFunc("GET /a/{id}/history", s.pageHistory)
	m.HandleFunc("GET /a/{id}/diff", s.pageDiff)
	m.HandleFunc("GET /a/{id}/source", s.pageSource)
	m.HandleFunc("GET /c/{id}/{ver}/{tok}/{path...}", s.serveContent)
	m.Handle("GET /_lib/", http.StripPrefix("/_lib/", s.staticHandler(s.LibFS, "public, max-age=604800, immutable")))
	m.Handle("GET /_viewer/", http.StripPrefix("/_viewer/", s.staticHandler(s.ViewerFS, "public, max-age=3600")))
	m.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	m.Handle("GET /admin/", http.StripPrefix("/admin/", s.adminStatic()))

	// 技能包
	m.HandleFunc("GET /skill.tar.gz", s.skillTarball)
	m.HandleFunc("GET /skill/version", s.skillVersion)
	m.HandleFunc("GET /skill/{path...}", s.skillFile)
}

// ---- 中间件 ----

type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeErr(sw, &artifact.Error{Status: 500, Code: "internal", Msg: "服务器内部错误"})
				}
			}
			d := time.Since(start)
			if sw.status == 0 {
				sw.status = 200
			}
			if !strings.HasPrefix(r.URL.Path, "/_lib/") && r.URL.Path != "/healthz" {
				s.Metrics.Observe(sw.status, d)
			}
			if sw.status >= 500 || d > 2*time.Second {
				slog.Warn("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", d.Milliseconds(), "ip", s.clientIP(r))
			}
		}()
		h := sw.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if r.ContentLength > s.Cfg.MaxBodyMB<<20 {
			writeErr(sw, &artifact.Error{Status: 413, Code: "body_too_large", Msg: fmt.Sprintf("请求体超过 %d MB", s.Cfg.MaxBodyMB)})
			return
		}
		r.Body = http.MaxBytesReader(sw, r.Body, s.Cfg.MaxBodyMB<<20)
		next.ServeHTTP(sw, r)
	})
}

func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.Cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return strings.HasPrefix(s.Cfg.PublicURL, "https://")
}

func (s *Server) requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || (s.Cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- JSON 工具 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *artifact.Error
	if !errors.As(err, &ae) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			ae = &artifact.Error{Status: 413, Code: "body_too_large", Msg: fmt.Sprintf("请求体超过 %d 字节上限", mbe.Limit)}
		} else {
			slog.Error("internal error", "err", err)
			ae = &artifact.Error{Status: 500, Code: "internal", Msg: "服务器内部错误：" + err.Error()}
		}
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

func badRequest(msg string) error { return &artifact.Error{Status: 400, Code: "bad_request", Msg: msg} }

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return badRequest("请求体不是合法的 JSON：" + err.Error())
	}
	return nil
}

func (s *Server) staticHandler(fsys fs.FS, cache string) http.Handler {
	fsrv := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", cache)
		w.Header().Set("Access-Control-Allow-Origin", "*") // 沙箱页面是不透明源，字体等资源需要 CORS
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		fsrv.ServeHTTP(w, r)
	})
}

// LibExists 判断 /_lib/ 下是否有该文件。
func LibExists(fsys fs.FS) func(string) bool {
	return func(p string) bool {
		p = strings.TrimPrefix(p, "/")
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
		st, err := fs.Stat(fsys, p)
		return err == nil && !st.IsDir()
	}
}
