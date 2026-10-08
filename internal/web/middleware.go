package web

import (
	"cmp"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// contentSecurityPolicy は画面の CSP。スクリプトとスタイルは同梱のファイルだけを許可する。
// Pico CSS はアイコンを data: の SVG で埋め込んでいるので、画像は data: も許可する。
// htmx の eval とインラインのスタイル挿入は htmx-config（layout.html）で無効にしている。
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("Content-Security-Policy", contentSecurityPolicy)
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// statusRecorder はアクセスログのためにステータスコードを記録する。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap は http.ResponseController が元の ResponseWriter を使えるようにする。
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// accessLog はリクエストごとにアクセスログを出す。静的ファイルは debug レベルにする。
// クエリ文字列は出さない（検索条件などを残さないため）。
func (h *Handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if strings.HasPrefix(r.URL.Path, "/static/") {
			level = slog.LevelDebug
		}
		h.log.Log(r.Context(), level, "access",
			"method", r.Method,
			"path", r.URL.Path,
			"status", cmp.Or(rec.status, http.StatusOK),
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
