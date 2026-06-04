package main

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))
)

type loadingData struct {
	Subdomain   string
	Title       string
	Subtitle    string
	Icon        string
	ThemeBG     string
	ThemeFG     string
	ThemeDim    string
	ThemeAccent string
	ThemeBorder string
	Timeout     int
	ShowLogs    string
	ShowStats   string
	BootLines   template.JS
}

func (s *Docknap) serveLoading(w http.ResponseWriter, r *http.Request, cfg *Config) {
	if srec, ok := w.(*statusRecorder); ok && srec.headersSent() {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)

	theme := themes[cfg.Theme]
	if theme == nil {
		theme = themes["green"]
	}
	title := cfg.Title
	if title == "" {
		title = cfg.Subdomain
	}
	subtitle := cfg.Subtitle
	if subtitle == "" {
		subtitle = "service is starting up"
	}
	icon := cfg.Icon
	if icon == "" {
		icon = "◐"
	}
	showLogs := "true"
	if !cfg.ShowLogs {
		showLogs = "false"
	}
	showStats := "true"
	if !cfg.ShowStats {
		showStats = "false"
	}
	boot := cfg.BootMessages
	if len(boot) == 0 {
		boot = strings.Split(defaultBootMessages, "|")
	}
	bootJS := template.JS(bootJSON(boot))

	data := loadingData{
		Subdomain:   cfg.Subdomain,
		Title:       title,
		Subtitle:    subtitle,
		Icon:        icon,
		ThemeBG:     theme.BG,
		ThemeFG:     theme.FG,
		ThemeDim:    theme.Dim,
		ThemeAccent: theme.Accent,
		ThemeBorder: theme.Border,
		Timeout:     int(cfg.StartupTimeout.Seconds()),
		ShowLogs:    showLogs,
		ShowStats:   showStats,
		BootLines:   bootJS,
	}
	_ = templates.ExecuteTemplate(w, "loading.html", data)
}

func bootJSON(boot []string) string {
	out := "["
	for i, b := range boot {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", b)
	}
	out += "]"
	return out
}

type adminData struct {
	DocknapVersion string
}

type loginData struct {
	ErrBlock template.HTML
	Next     string
}

type notFoundData struct {
	Subdomain string
}

func (s *Docknap) renderAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = templates.ExecuteTemplate(w, "admin.html", adminData{DocknapVersion: version})
}

func (s *Docknap) renderLogin(w http.ResponseWriter, r *http.Request, errCode, next string) {
	if srec, ok := w.(*statusRecorder); ok && srec.headersSent() {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = templates.ExecuteTemplate(w, "login.html", loginData{
		ErrBlock: template.HTML(loginErrorBlock(errCode)),
		Next:     next,
	})
}

func (s *Docknap) renderNotFound(w http.ResponseWriter, r *http.Request, sub string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_ = templates.ExecuteTemplate(w, "notfound.html", notFoundData{Subdomain: sub})
}
