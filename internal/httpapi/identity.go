package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vps-artifact/internal/artifact"
	"vps-artifact/internal/auth"
	"vps-artifact/internal/store"
)

const sessionCookie = "artifact_session"
const sessionTTL = 14 * 24 * time.Hour

type keyHandler func(w http.ResponseWriter, r *http.Request, a artifact.Actor)

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// withKey 校验 API Key，并按用户限额限流。
func (s *Server) withKey(h keyHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, &artifact.Error{Status: 401, Code: "unauthorized", Msg: "缺少 API Key。请在请求头加 Authorization: Bearer art_xxx"})
			return
		}
		ip := s.clientIP(r)
		if s.Limiter.Blocked("badkey:"+ip, 30) {
			writeErr(w, &artifact.Error{Status: 429, Code: "rate_limited", Msg: "无效凭据尝试过多，请稍后再试"})
			return
		}
		var k *store.APIKey
		err := errNotFound
		if auth.LooksLikeKey(tok) {
			k, err = s.Store.KeyByHash(auth.HashToken(tok))
		}
		if err != nil || !k.Active(time.Now().Unix()) {
			s.Limiter.Allow("badkey:"+ip, 30)
			msg := "API Key 无效"
			if err == nil && k.RevokedAt != nil {
				msg = "API Key 已被吊销"
			} else if err == nil {
				msg = "API Key 已过期"
			}
			writeErr(w, &artifact.Error{Status: 401, Code: "unauthorized", Msg: msg})
			return
		}
		u, err := s.Store.UserByID(k.UserID)
		if err != nil || u.Disabled {
			writeErr(w, &artifact.Error{Status: 403, Code: "user_disabled", Msg: "该用户已被停用"})
			return
		}
		q := u.Quota(s.Store.Settings())
		if ok, wait := s.Limiter.Allow(fmt.Sprintf("key:%d", k.ID), q.ReqPerMin); !ok {
			w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
			writeErr(w, &artifact.Error{Status: 429, Code: "rate_limited", Msg: fmt.Sprintf("请求过于频繁（每分钟上限 %d 次），请 %d 秒后再试", q.ReqPerMin, int(wait.Seconds())+1)})
			return
		}
		if k.LastUsedAt == nil || time.Now().Unix()-*k.LastUsedAt > 60 || k.LastIP != ip {
			s.Store.TouchKey(k.ID, ip)
		}
		kid := k.ID
		h(w, r, artifact.Actor{User: u, KeyID: &kid, IP: ip})
	}
}

// sessionUser 从 cookie 读取登录用户；未登录返回 nil。
func (s *Server) sessionUser(r *http.Request) (*store.User, string) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	ss, err := s.Store.SessionByHash(auth.HashToken(c.Value))
	if err != nil || ss.ExpiresAt < time.Now().Unix() {
		return nil, ""
	}
	u, err := s.Store.UserByID(ss.UserID)
	if err != nil || u.Disabled {
		return nil, ""
	}
	return u, c.Value
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, a artifact.Actor)

// withSession 校验后台会话；写操作还要校验 CSRF 令牌和 Origin。
func (s *Server) withSession(adminOnly bool, h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, tok := s.sessionUser(r)
		if u == nil {
			writeErr(w, &artifact.Error{Status: 401, Code: "unauthorized", Msg: "请先登录"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.sameOrigin(r) {
				writeErr(w, &artifact.Error{Status: 403, Code: "bad_origin", Msg: "请求来源不被允许"})
				return
			}
			if !s.Signer.VerifyCSRF(tok, r.Header.Get("X-CSRF-Token")) {
				writeErr(w, &artifact.Error{Status: 403, Code: "bad_csrf", Msg: "页面已过期，请刷新后重试"})
				return
			}
		}
		if adminOnly && !u.IsAdmin() {
			writeErr(w, &artifact.Error{Status: 403, Code: "forbidden", Msg: "需要管理员权限"})
			return
		}
		h(w, r, artifact.Actor{User: u, IP: s.clientIP(r)})
	}
}

func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			return true // 非浏览器客户端
		}
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		o = u.Scheme + "://" + u.Host
	}
	if o == "null" {
		return false
	}
	return strings.EqualFold(o, s.requestOrigin(r)) || strings.EqualFold(o, s.Cfg.Origin())
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, u *store.User) (string, error) {
	tok := auth.NewSessionToken()
	exp := time.Now().Add(sessionTTL)
	if err := s.Store.CreateSession(auth.HashToken(tok), u.ID, exp.Unix(), s.clientIP(r), r.UserAgent()); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		Secure: s.isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return tok, nil
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// canManage 判断用户能否查看历史版本、源码和对比。
func canManage(u *store.User, art *store.Artifact) bool {
	return u != nil && (u.IsAdmin() || u.ID == art.OwnerID)
}

var errNotFound = errors.New("not found")
