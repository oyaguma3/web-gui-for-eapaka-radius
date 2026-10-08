package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// pages はページ名（templates/pages/ のファイル名から拡張子を除いたもの）ごとのテンプレート。
// 各ページは templates/layout.html の "layout" を実行し、ページ側で "content" を定義する。
type pages map[string]*template.Template

func parsePages() (pages, error) {
	base, err := template.ParseFS(assets, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	p := pages{}
	for _, file := range files {
		t, err := template.Must(base.Clone()).ParseFS(assets, file)
		if err != nil {
			return nil, err
		}
		p[strings.TrimSuffix(path.Base(file), ".html")] = t
	}
	return p, nil
}

// view はテンプレートに渡す値。
type view struct {
	Title   string
	Version string
	// Data はページごとの値。
	Data any
}

// render はページを描画して返す。描画が終わってから書き出すので、途中で失敗しても壊れた HTML は返さない。
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
	t, ok := h.pages[page]
	if !ok {
		h.log.Error("unknown page template", "page", page)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", view{Title: title, Version: h.version, Data: data}); err != nil {
		h.log.Error("render template", "page", page, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Content-Length", strconv.Itoa(buf.Len()))
	// 画面には加入者情報や鍵情報が載るので、ブラウザにもキャッシュさせない。
	hdr.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}

// errorData はエラーページに渡す値。
type errorData struct {
	Status  int
	Message string
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.render(w, r, status, "error", http.StatusText(status), errorData{Status: status, Message: message})
}
