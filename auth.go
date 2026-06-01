package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

const authRealm = "docknap"

func (s *Docknap) authEnabled() bool {
	return s.adminUser != "" && len(s.adminPassHash) > 0
}

func (s *Docknap) hashPassword(pass string) []byte {
	sum := sha256.Sum256([]byte(pass))
	return sum[:]
}

func (s *Docknap) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			next(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Basic ") {
			s.failAuth(w, r, "missing")
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
		if err != nil {
			s.failAuth(w, r, "malformed")
			return
		}
		user, pass, ok := strings.Cut(string(decoded), ":")
		if !ok {
			s.failAuth(w, r, "malformed")
			return
		}
		userHash := sha256.Sum256([]byte(user))
		passHash := sha256.Sum256([]byte(pass))
		userMatch := subtle.ConstantTimeCompare(userHash[:], s.adminUserHash) == 1
		passMatch := subtle.ConstantTimeCompare(passHash[:], s.adminPassHash) == 1
		if !userMatch || !passMatch {
			s.failAuth(w, r, "invalid")
			return
		}
		next(w, r)
	}
}

func (s *Docknap) failAuth(w http.ResponseWriter, r *http.Request, reason string) {
	if s.mAuthFail != nil {
		s.mAuthFail.Add(map[string]string{"path": r.URL.Path, "reason": reason}, 1)
	}
	s.logger.Warn("auth failed", F("path", r.URL.Path), F("reason", reason), F("remote", r.RemoteAddr))
	w.Header().Set("WWW-Authenticate", `Basic realm="`+authRealm+`", charset="UTF-8"`)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("401 Unauthorized\n"))
}
