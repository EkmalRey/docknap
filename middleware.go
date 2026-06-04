package main

import (
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
