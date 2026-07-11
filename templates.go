package main

import (
	"embed"
	"encoding/json"
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
	Nonce       string
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
	bootJS := template.JS(bootJSON(boot)) //nolint:gosec // G203: boot messages are repository-owned config strings, not user input

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
		Nonce:       requestNonce(r),
	}
	_ = templates.ExecuteTemplate(w, "loading.html", data)
}

func bootJSON(boot []string) string {
	b, err := json.Marshal(boot)
	if err != nil {
		return "[]"
	}
	return string(b)
}

type adminData struct {
	DocknapVersion string
	CSRFToken      string
	Nonce          string
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
	_ = templates.ExecuteTemplate(w, "admin.html", adminData{
		DocknapVersion: version,
		CSRFToken:      s.renderAdminCtx(w, r),
		Nonce:          requestNonce(r),
	})
}

func (s *Docknap) renderLogin(w http.ResponseWriter, r *http.Request, errCode, next string) {
	if srec, ok := w.(*statusRecorder); ok && srec.headersSent() {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = templates.ExecuteTemplate(w, "login.html", loginData{
		ErrBlock: template.HTML(loginErrorBlock(errCode)), //nolint:gosec // G203: error block is html.EscapeString'd inside loginErrorBlock
		Next:     next,
	})
}

func (s *Docknap) renderNotFound(w http.ResponseWriter, r *http.Request, sub string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_ = templates.ExecuteTemplate(w, "notfound.html", notFoundData{Subdomain: sub})
}
