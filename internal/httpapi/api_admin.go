package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vps-artifact/internal/artifact"
	"vps-artifact/internal/auth"
	"vps-artifact/internal/skillpack"
	"vps-artifact/internal/store"
)

func (s *Server) adminRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/admin/login", s.adminLogin)
	m.HandleFunc("POST /api/admin/logout", s.adminLogout)
	m.HandleFunc("GET /api/admin/me", s.adminMe)

	m.HandleFunc("GET /api/admin/overview", s.withSession(false, s.adminOverview))
	m.HandleFunc("GET /api/admin/artifacts", s.withSession(false, s.adminArtifacts))
	m.HandleFunc("GET /api/admin/artifacts/{id}", s.withSession(false, s.adminArtifact))
	m.HandleFunc("PATCH /api/admin/artifacts/{id}", s.withSession(false, s.adminArtifactPatch))
	m.HandleFunc("DELETE /api/admin/artifacts/{id}", s.withSession(false, s.adminArtifactDelete))
	m.HandleFunc("POST /api/admin/artifacts/{id}/undelete", s.withSession(false, s.adminArtifactUndelete))
	m.HandleFunc("DELETE /api/admin/artifacts/{id}/purge", s.withSession(false, s.adminArtifactPurge))
	m.HandleFunc("POST /api/admin/artifacts/{id}/rollback", s.withSession(false, s.adminArtifactRollback))
	m.HandleFunc("GET /api/admin/artifacts/{id}/diff", s.withSession(false, s.adminArtifactDiff))

	m.HandleFunc("GET /api/admin/keys", s.withSession(false, s.adminKeys))
	m.HandleFunc("POST /api/admin/keys", s.withSession(false, s.adminKeyCreate))
	m.HandleFunc("DELETE /api/admin/keys/{id}", s.withSession(false, s.adminKeyRevoke))
	m.HandleFunc("POST /api/admin/password", s.withSession(false, s.adminChangePassword))
	m.HandleFunc("GET /api/admin/prompt", s.withSession(false, s.adminPromptTemplate))

	m.HandleFunc("GET /api/admin/users", s.withSession(true, s.adminUsers))
	m.HandleFunc("POST /api/admin/users", s.withSession(true, s.adminUserCreate))
	m.HandleFunc("PATCH /api/admin/users/{id}", s.withSession(true, s.adminUserPatch))
	m.HandleFunc("POST /api/admin/users/{id}/password", s.withSession(true, s.adminUserPassword))
	m.HandleFunc("DELETE /api/admin/users/{id}", s.withSession(true, s.adminUserDelete))
	m.HandleFunc("GET /api/admin/settings", s.withSession(true, s.adminSettings))
	m.HandleFunc("PUT /api/admin/settings", s.withSession(true, s.adminSettingsSave))
	m.HandleFunc("GET /api/admin/audit", s.withSession(true, s.adminAudit))
	m.HandleFunc("POST /api/admin/gc", s.withSession(true, s.adminGC))
	m.HandleFunc("/api/admin/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "没有这个接口"})
	})
}

func userJSON(u *store.User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "display_name": u.DisplayName, "role": u.Role}
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Limiter.Blocked("login:"+ip, 10) {
		writeErr(w, &artifact.Error{Status: 429, Code: "rate_limited", Msg: "登录失败次数过多，请几分钟后再试"})
		return
	}
	if !s.sameOrigin(r) {
		writeErr(w, &artifact.Error{Status: 403, Code: "bad_origin", Msg: "请求来源不被允许"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.Store.UserByName(req.Username)
	hash := ""
	if err == nil {
		hash = u.PasswordHash
	}
	if !auth.CheckPassword(hash, req.Password) || err != nil || u.Disabled {
		s.Limiter.Allow("login:"+ip, 10)
		s.Store.Audit(nil, nil, ip, "login.fail", req.Username, "")
		writeErr(w, &artifact.Error{Status: 401, Code: "bad_credentials", Msg: "用户名或密码不正确"})
		return
	}
	tok, err := s.setSession(w, r, u)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&u.ID, nil, ip, "login", u.Name, "")
	writeJSON(w, 200, map[string]any{"user": userJSON(u), "csrf": s.Signer.CSRFToken(tok)})
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w, r)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminMe(w http.ResponseWriter, r *http.Request) {
	u, tok := s.sessionUser(r)
	if u == nil {
		writeJSON(w, 200, map[string]any{"user": nil})
		return
	}
	st := s.Store.Settings()
	writeJSON(w, 200, map[string]any{
		"user": userJSON(u), "csrf": s.Signer.CSRFToken(tok), "server": s.Cfg.PublicURL,
		"version": s.Version, "allow_self_keys": st.AllowSelfKeys || u.IsAdmin(), "has_password": u.HasPassword,
	})
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	st := s.Store.Settings()
	used, _ := s.Store.UserStorage(a.User.ID)
	out := map[string]any{
		"me": map[string]any{
			"quota": a.User.Quota(st), "storage_bytes": used, "artifacts": s.Store.CountArtifacts(a.User.ID),
			"publishes_14d": s.Store.PublishesPerDay(14, a.User.ID),
		},
	}
	if a.User.IsAdmin() {
		out["system"] = s.Metrics.System(s.Cfg.DataDir)
		out["series"] = s.Metrics.Series()
		out["counts"] = s.Store.Counts()
		out["db_bytes"] = s.Store.DBSize()
		out["publishes_14d"] = s.Store.PublishesPerDay(14, 0)
		out["recent"] = s.Store.RecentVersions(0, 12)
		out["min_free_disk_mb"] = s.Cfg.MinFreeDiskMB
		users, _ := s.Store.ListUsers()
		type top struct {
			Name    string `json:"name"`
			Storage int64  `json:"storage_bytes"`
			Limit   int64  `json:"limit_bytes"`
			Count   int64  `json:"artifacts"`
		}
		tops := []top{}
		for _, u := range users {
			tops = append(tops, top{u.Name, u.StorageBytes, u.Quota(st).StorageBytes, u.ArtifactCount})
		}
		for i := 0; i < len(tops); i++ {
			for j := i + 1; j < len(tops); j++ {
				if tops[j].Storage > tops[i].Storage {
					tops[i], tops[j] = tops[j], tops[i]
				}
			}
		}
		if len(tops) > 8 {
			tops = tops[:8]
		}
		out["top_users"] = tops
	} else {
		out["recent"] = s.Store.RecentVersions(a.User.ID, 12)
	}
	writeJSON(w, 200, out)
}

func (s *Server) adminArtifacts(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	q := r.URL.Query()
	lq := store.ListQuery{Query: q.Get("q"), Visibility: q.Get("visibility"), Kind: q.Get("kind"), Sort: q.Get("sort"), Deleted: q.Get("deleted") == "1"}
	lq.Limit, _ = strconv.Atoi(q.Get("limit"))
	lq.Offset, _ = strconv.Atoi(q.Get("offset"))
	if a.User.IsAdmin() {
		lq.OwnerID, _ = strconv.ParseInt(q.Get("owner"), 10, 64)
	} else {
		lq.OwnerID = a.User.ID
	}
	res, err := s.Store.ListArtifacts(lq)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) adminArtifact(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	art, err := s.Svc.Load(a, r.PathValue("id"), false)
	if err != nil {
		writeErr(w, err)
		return
	}
	vs, _ := s.Store.Versions(art.ID)
	files, _ := s.Store.VersionFiles(art.ID, art.CurrentVersion)
	tok := s.Signer.ContentToken(art.ID, art.CurrentVersion, 6*time.Hour)
	writeJSON(w, 200, map[string]any{
		"artifact": art, "versions": vs, "files": files, "url": s.Svc.ViewURL(art.ID),
		"preview": s.contentURL(art.ID, art.CurrentVersion, tok, ""),
	})
}

func (s *Server) adminArtifactPatch(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var p artifact.MetaPatch
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	art, err := s.Svc.UpdateMeta(a, r.PathValue("id"), p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, art)
}

func (s *Server) adminArtifactDelete(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	if err := s.Svc.Delete(a, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminArtifactUndelete(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	if err := s.Svc.Undelete(a, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminArtifactPurge(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	if err := s.Svc.Purge(a, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminArtifactRollback(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
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

func (s *Server) adminArtifactDiff(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
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
	d, err := s.Svc.DiffVersions(art.ID, from, to, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// ---- 密钥 ----

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	uid := a.User.ID
	if a.User.IsAdmin() {
		uid, _ = strconv.ParseInt(r.URL.Query().Get("user"), 10, 64)
	}
	keys, err := s.Store.ListKeys(uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": keys})
}

// IssueKey 为用户签发密钥并生成接入提示词。
func (s *Server) IssueKey(userID int64, name string, expiresDays int) (string, *store.APIKey, error) {
	plain, prefix, hash := auth.NewAPIKey()
	var exp *int64
	if expiresDays > 0 {
		t := time.Now().Add(time.Duration(expiresDays) * 24 * time.Hour).Unix()
		exp = &t
	}
	k, err := s.Store.CreateKey(userID, name, prefix, hash, exp)
	return plain, k, err
}

func (s *Server) adminKeyCreate(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var req struct {
		UserID      int64  `json:"user_id"`
		Name        string `json:"name"`
		ExpiresDays int    `json:"expires_days"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.UserID == 0 {
		req.UserID = a.User.ID
	}
	if req.UserID != a.User.ID && !a.User.IsAdmin() {
		writeErr(w, &artifact.Error{Status: 403, Code: "forbidden", Msg: "只能为自己签发密钥"})
		return
	}
	if !a.User.IsAdmin() && !s.Store.Settings().AllowSelfKeys {
		writeErr(w, &artifact.Error{Status: 403, Code: "forbidden", Msg: "管理员关闭了自助签发密钥，请联系管理员"})
		return
	}
	u, err := s.Store.UserByID(req.UserID)
	if err != nil {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "用户不存在"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "agent"
	}
	plain, k, err := s.IssueKey(u.ID, truncateStr(name, 60), req.ExpiresDays)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "key.issue", u.Name, fmt.Sprintf("%s（%s）", k.Prefix, name))
	writeJSON(w, 201, map[string]any{"key": plain, "info": k, "prompt": skillpack.Prompt(s.Cfg.PublicURL, plain, u.Name)})
}

func (s *Server) adminKeyRevoke(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	k, err := s.Store.KeyByID(id)
	if err != nil || (k.UserID != a.User.ID && !a.User.IsAdmin()) {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "密钥不存在"})
		return
	}
	if err := s.Store.RevokeKey(id); err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "key.revoke", k.UserName, k.Prefix)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminPromptTemplate(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	writeJSON(w, 200, map[string]any{"prompt": skillpack.Prompt(s.Cfg.PublicURL, "", a.User.Name)})
}

func (s *Server) adminChangePassword(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if a.User.HasPassword && !auth.CheckPassword(a.User.PasswordHash, req.Old) {
		writeErr(w, &artifact.Error{Status: 400, Code: "bad_password", Msg: "原密码不正确"})
		return
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	if err := s.Store.SetPassword(a.User.ID, h); err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "user.password", a.User.Name, "")
	tok, _ := s.setSession(w, r, a.User)
	writeJSON(w, 200, map[string]any{"ok": true, "csrf": s.Signer.CSRFToken(tok)})
}

// ---- 用户（管理员） ----

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	users, err := s.Store.ListUsers()
	if err != nil {
		writeErr(w, err)
		return
	}
	st := s.Store.Settings()
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"user": u.User, "artifacts": u.ArtifactCount, "storage_bytes": u.StorageBytes, "keys": u.KeyCount,
			"last_active_at": u.LastActiveAt, "quota": u.Quota(st),
		})
	}
	writeJSON(w, 200, map[string]any{"items": out, "defaults": st})
}

var userNameOK = func(n string) bool {
	if len(n) < 2 || len(n) > 32 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Note        string `json:"note"`
		IssueKey    bool   `json:"issue_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !userNameOK(req.Name) {
		writeErr(w, badRequest("用户名需 2–32 位，只能包含字母、数字、- _ ."))
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role != "user" && req.Role != "admin" {
		writeErr(w, badRequest("角色必须是 user 或 admin"))
		return
	}
	hash := ""
	if req.Password != "" {
		h, err := auth.HashPassword(req.Password)
		if err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		hash = h
	}
	u, err := s.Store.CreateUser(store.NewUser{Name: req.Name, DisplayName: strings.TrimSpace(req.DisplayName), PasswordHash: hash, Role: req.Role, Note: req.Note})
	if err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "user.create", u.Name, u.Role)
	out := map[string]any{"user": u}
	if req.IssueKey {
		plain, k, err := s.IssueKey(u.ID, "agent", 0)
		if err == nil {
			s.Store.Audit(&a.User.ID, nil, a.IP, "key.issue", u.Name, k.Prefix)
			out["key"] = plain
			out["prompt"] = skillpack.Prompt(s.Cfg.PublicURL, plain, u.Name)
		}
	}
	writeJSON(w, 201, out)
}

func (s *Server) adminUserPatch(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := s.Store.UserByID(id)
	if err != nil {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "用户不存在"})
		return
	}
	var p store.UserPatch
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	// 不允许让系统失去最后一个可用管理员
	if u.IsAdmin() && ((p.Role != nil && *p.Role != "admin") || (p.Disabled != nil && *p.Disabled)) {
		if n, _ := s.Store.CountAdmins(); n <= 1 {
			writeErr(w, badRequest("这是最后一个可用的管理员，不能降级或停用"))
			return
		}
	}
	if err := s.Store.UpdateUser(id, p); err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	if p.Disabled != nil && *p.Disabled {
		s.Store.DB.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "user.update", u.Name, "")
	nu, _ := s.Store.UserByID(id)
	writeJSON(w, 200, nu)
}

func (s *Server) adminUserPassword(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := s.Store.UserByID(id)
	if err != nil {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "用户不存在"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	if err := s.Store.SetPassword(id, h); err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "user.password", u.Name, "管理员重置")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u, err := s.Store.UserByID(id)
	if err != nil {
		writeErr(w, &artifact.Error{Status: 404, Code: "not_found", Msg: "用户不存在"})
		return
	}
	if u.ID == a.User.ID {
		writeErr(w, badRequest("不能删除自己"))
		return
	}
	if u.IsAdmin() {
		if n, _ := s.Store.CountAdmins(); n <= 1 {
			writeErr(w, badRequest("这是最后一个可用的管理员，不能删除"))
			return
		}
	}
	ids := s.Store.ArtifactIDsByOwner(id)
	for _, aid := range ids {
		if err := s.Store.Purge(aid); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := s.Store.DeleteUser(id); err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "user.delete", u.Name, fmt.Sprintf("同时删除 %d 个 artifact", len(ids)))
	go s.Svc.GC()
	writeJSON(w, 200, map[string]any{"ok": true, "purged_artifacts": len(ids)})
}

// ---- 设置、审计、GC ----

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	writeJSON(w, 200, map[string]any{"settings": s.Store.Settings(), "defaults": store.DefaultSettings()})
}

func (s *Server) adminSettingsSave(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	st := s.Store.Settings()
	if err := decodeJSON(r, &st); err != nil {
		writeErr(w, err)
		return
	}
	clean := []string{}
	for _, o := range st.CDNAllowlist {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		if !strings.HasPrefix(o, "https://") {
			writeErr(w, badRequest("CDN 白名单只接受 https:// 开头的源："+o))
			return
		}
		clean = append(clean, o)
	}
	st.CDNAllowlist = clean
	if err := s.Store.SaveSettings(st); err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "settings.update", "", "")
	writeJSON(w, 200, map[string]any{"settings": st})
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	q := r.URL.Query()
	aq := store.AuditQuery{Action: q.Get("action"), Target: q.Get("target")}
	aq.UserID, _ = strconv.ParseInt(q.Get("user"), 10, 64)
	aq.Limit, _ = strconv.Atoi(q.Get("limit"))
	aq.Offset, _ = strconv.Atoi(q.Get("offset"))
	items, total, err := s.Store.ListAudit(aq)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

func (s *Server) adminGC(w http.ResponseWriter, r *http.Request, a artifact.Actor) {
	n, freed, err := s.Svc.GC()
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Store.Audit(&a.User.ID, nil, a.IP, "gc", "", fmt.Sprintf("删除 %d 个文件，释放 %s", n, auth.FormatBytes(freed)))
	writeJSON(w, 200, map[string]any{"deleted": n, "freed_bytes": freed})
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
