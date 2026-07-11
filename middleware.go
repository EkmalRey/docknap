package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"runtime/debug"
)

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				defaultLogger.Error("panic in handler",
					F("path", r.URL.Path),
					F("method", r.Method),
					F("err", anyToString(rec)),
					F("stack", string(debug.Stack())),
				)
				if rw, ok := w.(interface{ WriteHeader(int) }); ok {
					rw.WriteHeader(http.StatusInternalServerError)
				} else {
					w.WriteHeader(http.StatusInternalServerError)
				}
				_, _ = w.Write([]byte("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func anyToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "non-string panic"
}

type ctxKey int

const nonceKey ctxKey = iota

// requestNonce returns the per-request CSP nonce generated in securityHeaders.
func requestNonce(r *http.Request) string {
	if n, ok := r.Context().Value(nonceKey).(string); ok {
		return n
	}
	return ""
}

// securityHeaders sets baseline browser hardening headers on every response.
// A fresh per-request nonce is minted and required for inline scripts, so the
// CSP actually constrains script execution (audit #13) rather than allowing
// 'unsafe-inline' everywhere.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce, err := makeNonce()
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), nonceKey, nonce))
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

var nonceReader io.Reader = rand.Reader

func makeNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(nonceReader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
