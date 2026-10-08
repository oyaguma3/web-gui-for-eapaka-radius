// Package web はブラウザ向けの画面（HTML）と静的ファイルを返す。
//
// 同梱しているサードパーティのファイル（static/）:
//   - htmx.min.js: htmx 2.0.11（https://htmx.org、0BSD）
//   - pico.min.css: Pico CSS 2.1.1（https://picocss.com、MIT。著作権表示はファイル先頭）
//
// 更新するときは npm の配布物（jsdelivr）から同じファイル名で置き換え、上の版数も直す。
package web

import (
	"embed"
	"fmt"
	"log/slog"
	"net/http"
)

//go:embed templates static
var assets embed.FS

// Handler は画面のハンドラー。
type Handler struct {
	log     *slog.Logger
	version string
	pages   pages
	static  *staticFiles
}

// Options は Handler の設定。
type Options struct {
	Log *slog.Logger
	// Version は画面のフッターに出すバージョン。
	Version string
}

// New は Handler を作る。テンプレートと静的ファイルはここで読み込む。
func New(opts Options) (*Handler, error) {
	h := &Handler{log: opts.Log, version: opts.Version}
	var err error
	if h.pages, err = parsePages(); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	if h.static, err = newStaticFiles(http.HandlerFunc(h.notFound)); err != nil {
		return nil, fmt.Errorf("load static files: %w", err)
	}
	return h, nil
}

// Routes はルーティングとミドルウェアを組み立てた http.Handler を返す。
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", h.static)
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("/", h.notFound)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.log.Warn("cross-origin request rejected",
			"method", r.Method, "path", r.URL.Path, "origin", r.Header.Get("Origin"))
		h.renderError(w, r, http.StatusForbidden, "別のサイトからの操作は受け付けません。")
	}))

	var handler http.Handler = mux
	handler = cop.Handler(handler)
	handler = securityHeaders(handler)
	handler = h.accessLog(handler)
	return handler
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "home", "ホーム", nil)
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusNotFound, "ページが見つかりません。")
}
