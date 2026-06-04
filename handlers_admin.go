package main

import "net/http"

func (s *Docknap) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Docknap) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/_docknap" && r.URL.Path != "/_docknap/" && r.URL.Path != "/_docknap/ui" {
		http.NotFound(w, r)
		return
	}
	s.renderAdmin(w, r)
}
