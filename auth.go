package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

const (
	authRealm        = "docknap"
	authCookieName   = "docknap_auth"
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

// requireAuth gates admin endpoints. A request is authenticated if it carries
// either a valid HTTP Basic Auth header (Authorization: Basic ...) or a
// valid docknap_auth session cookie set by the custom login form. On
// failure, it serves the themed login page; we deliberately do not set
// WWW-Authenticate so the browser's native dialog does not appear.
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

func (s *Docknap) checkRequestAuth(r *http.Request) bool {
	if user, pass, ok := parseBasicAuth(r.Header.Get("Authorization")); ok {
		return s.verifyCredentials(user, pass)
	}
	if cookie, err := r.Cookie(authCookieName); err == nil && cookie.Value != "" {
		if decoded, derr := base64.StdEncoding.DecodeString(cookie.Value); derr == nil {
			if user, pass, ok := strings.Cut(string(decoded), ":"); ok {
				return s.verifyCredentials(user, pass)
			}
		}
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
	if s.mAuthFail != nil {
		s.mAuthFail.Add(map[string]string{"path": r.URL.Path, "reason": reason}, 1)
	}
	s.logger.Warn("auth failed", F("path", r.URL.Path), F("reason", reason), F("remote", r.RemoteAddr))
	s.serveLogin(w, r, "invalid")
}

// handleLogin serves the themed login form on GET and processes credentials
// on POST. The endpoint is intentionally NOT wrapped by requireAuth; it is
// the entry point for unauthenticated users.
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
		if s.mAuthFail != nil {
			s.mAuthFail.Add(map[string]string{"path": r.URL.Path, "reason": "invalid"}, 1)
		}
		s.logger.Warn("auth failed",
			F("path", r.URL.Path),
			F("reason", "invalid"),
			F("remote", r.RemoteAddr),
		)
		s.failLogin(w, r, next, "invalid")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    base64.StdEncoding.EncodeToString([]byte(user + ":" + pass)),
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(authCookieMaxAge.Seconds()),
	})

	s.logger.Info("admin login", F("user", user), F("remote", r.RemoteAddr))
	http.Redirect(w, r, safeRedirect(next, "/_docknap/"), http.StatusFound)
}

func (s *Docknap) failLogin(w http.ResponseWriter, r *http.Request, next, errCode string) {
	target := safeRedirect(next, loginPath)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+"error="+errCode, http.StatusFound)
}

func (s *Docknap) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, loginPath, http.StatusFound)
}

// requestIsHTTPS reports whether the request reached us over HTTPS, taking
// a standard X-Forwarded-Proto header from a TLS-terminating reverse proxy
// into account. Used to set the Secure flag on cookies.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// safeRedirect validates that next is a relative same-origin path so the
// login form can be used to bounce a user back to where they came from
// without enabling an open redirect. Returns defaultPath if next is unsafe.
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

func (s *Docknap) renderLogin(w http.ResponseWriter, r *http.Request, errMsg, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	page := strings.NewReplacer(
		"{ERR_BLOCK}", loginErrorBlock(errMsg),
		"{NEXT}", htmlEscape(next),
	).Replace(loginPage)
	w.Write([]byte(page))
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
