package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	authRealm        = "docknap"
	authCookieName   = "docknap_auth"
	csrfCookieName   = "docknap_csrf"
	csrfHeaderName   = "X-CSRF-Token"
	csrfFormField    = "csrf"
	authCookieMaxAge = 12 * time.Hour
	loginPath        = "/_docknap/auth/login"
	logoutPath       = "/_docknap/auth/logout"
)

func (s *Docknap) authEnabled() bool {
	return s.adminUser != "" && len(s.adminPassHash) > 0
}

func (s *Docknap) hashPassword(pass string) []byte {
	sum := sha256.Sum256([]byte(pass))
	return sum[:]
}

func (s *Docknap) verifyCredentials(user, pass string) bool {
	userHash := sha256.Sum256([]byte(user))
	passHash := sha256.Sum256([]byte(pass))
	userMatch := subtle.ConstantTimeCompare(userHash[:], s.adminUserHash) == 1
	passMatch := subtle.ConstantTimeCompare(passHash[:], s.adminPassHash) == 1
	return userMatch && passMatch
}

func (s *Docknap) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			next(w, r)
			return
		}
		if s.checkRequestAuth(r) {
			next(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			s.failAuth(w, r, "invalid")
			return
		}
		s.serveLogin(w, r, "")
	}
}

func (s *Docknap) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() || r.Method != http.MethodPost {
			next(w, r)
			return
		}
		// Only enforce CSRF when a session-cookie login is in use. Basic-auth
		// requests are authenticated by the Authorization header, which an
		// attacker cannot forge cross-origin.
		if r.Header.Get("Authorization") != "" {
			next(w, r)
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			s.logger.Warn("csrf rejected: missing token",
				F("path", r.URL.Path), F("remote", r.RemoteAddr))
			http.Error(w, "csrf token missing", http.StatusForbidden)
			return
		}
		headerToken := r.Header.Get(csrfHeaderName)
		if headerToken == "" {
			headerToken = r.PostFormValue(csrfFormField)
		}
		if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) != 1 {
			s.logger.Warn("csrf rejected: token mismatch",
				F("path", r.URL.Path), F("remote", r.RemoteAddr))
			http.Error(w, "csrf token invalid", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Docknap) checkRequestAuth(r *http.Request) bool {
	if user, pass, ok := parseBasicAuth(r.Header.Get("Authorization")); ok {
		return s.verifyCredentials(user, pass)
	}
	if cookie, err := r.Cookie(authCookieName); err == nil && cookie.Value != "" {
		return s.sessions.valid(cookie.Value)
	}
	return false
}

func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(decoded), ":")
	if !ok {
		return "", "", false
	}
	return user, pass, true
}

func (s *Docknap) failAuth(w http.ResponseWriter, r *http.Request, reason string) {
	if s.m.AuthFail != nil {
		s.m.AuthFail.Add(map[string]string{"path": r.URL.Path, "reason": reason}, 1)
	}
	s.logger.Warn("auth failed", F("path", r.URL.Path), F("reason", reason), F("remote", r.RemoteAddr))
	s.serveLogin(w, r, "invalid")
}

func (s *Docknap) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodPost {
		s.processLogin(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, POST")
		s.renderLogin(w, r, "method", r.URL.Query().Get("next"))
		return
	}

	if s.checkRequestAuth(r) {
		http.Redirect(w, r, safeRedirect(r.URL.Query().Get("next"), "/_docknap/"), http.StatusFound)
		return
	}

	s.renderLogin(w, r, r.URL.Query().Get("error"), r.URL.Query().Get("next"))
}

func (s *Docknap) processLogin(w http.ResponseWriter, r *http.Request) {
	remoteKey := s.clientKey(r)
	if !s.rateLimiter.allow(remoteKey) {
		s.m.AuthFail.Add(map[string]string{"path": r.URL.Path, "reason": "rate_limited"}, 1)
		s.logger.Warn("login rate-limited", F("remote", r.RemoteAddr))
		s.failLogin(w, r, "", "rate_limited")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, "bad_request", "")
		return
	}
	user := r.PostForm.Get("user")
	pass := r.PostForm.Get("pass")
	next := r.PostForm.Get("next")

	if user == "" || pass == "" {
		s.failLogin(w, r, next, "missing")
		return
	}

	if !s.verifyCredentials(user, pass) {
		s.m.AuthFail.Add(map[string]string{"path": r.URL.Path, "reason": "invalid"}, 1)
		s.logger.Warn("auth failed",
			F("path", r.URL.Path),
			F("reason", "invalid"),
			F("remote", r.RemoteAddr),
		)
		s.failLogin(w, r, next, "invalid")
		return
	}

	tok, err := s.sessions.issue()
	if err != nil {
		s.logger.Error("session issue failed", F("err", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(authCookieMaxAge.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: false,
		Secure:   s.requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(authCookieMaxAge.Seconds()),
	})
	s.logger.Info("admin login",
		F("user", user),
		F("remote", r.RemoteAddr),
		F("method", "session_cookie"),
	)
	http.Redirect(w, r, safeRedirect(next, "/_docknap/"), http.StatusFound)
}

func (s *Docknap) clientKey(r *http.Request) string {
	if s.trustedProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
}

func (s *Docknap) failLogin(w http.ResponseWriter, r *http.Request, next, errCode string) {
	target := safeRedirect(next, loginPath)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"error="+url.QueryEscape(errCode), http.StatusFound)
}

func (s *Docknap) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(authCookieName); err == nil {
		s.sessions.revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: false,
		Secure:   s.requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, loginPath, http.StatusFound)
}

func (s *Docknap) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !s.trustedProxy(r) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func safeRedirect(next, defaultPath string) string {
	if next == "" {
		return defaultPath
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return defaultPath
	}
	if strings.Contains(next, "://") {
		return defaultPath
	}
	return next
}

func (s *Docknap) serveLogin(w http.ResponseWriter, r *http.Request, errMsg string) {
	s.renderLogin(w, r, errMsg, r.URL.Query().Get("next"))
}

func loginErrorBlock(errCode string) string {
	if errCode == "" {
		return ""
	}
	var friendly string
	switch errCode {
	case "invalid":
		friendly = "invalid credentials"
	case "missing":
		friendly = "username and password required"
	case "method":
		friendly = "unsupported method"
	case "bad_request":
		friendly = "bad request"
	case "rate_limited":
		friendly = "too many attempts, try again shortly"
	default:
		friendly = errCode
	}
	return `<div class="err">[!] ` + htmlEscape(friendly) + `</div>`
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
