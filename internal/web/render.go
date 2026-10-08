package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
)

// pages はページ名（templates/pages/ のファイル名から拡張子を除いたもの）ごとのテンプレート。
// 各ページは templates/layout.html の "layout" を実行し、ページ側で "content" を定義する。
// ページごとに別々に解析するので、ページのファイルで定義したテンプレートは他のページからは使えない。
type pages map[string]*template.Template

// funcs はテンプレートで使う関数。
var funcs = template.FuncMap{
	// keysPlaceholder は、Ki / OPc をまだ表示していない状態の値を作る。
	"keysPlaceholder": func(imsi string) keysData { return keysData{IMSI: imsi} },
	// secretPlaceholder は、共有シークレットをまだ表示していない状態の値を作る。
	"secretPlaceholder": func(id int64) secretData { return secretData{ID: id} },
	// actionLabel は監査ログの操作の名前を返す。
	"actionLabel": actionLabel,
	// minPasswordLen はパスワードの最小文字数。入力欄の制限と説明に使う。
	"minPasswordLen": func() int { return auth.MinPasswordLen },
	// datetime は日時を BFF のタイムゾーン（環境変数 TZ）で表示する。
	"datetime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04:05 MST")
	},
}

func parsePages() (pages, error) {
	// missingkey=zero: 入力の誤り（fieldErrors）のように、マップにない項目を空文字列として扱う。
	base, err := template.New("layout.html").Funcs(funcs).Option("missingkey=zero").
		ParseFS(assets, "templates/layout.html")
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
	// User はログイン中のアカウント。ログインしていなければ nil。
	User *auth.Account
	// Data はページごとの値。
	Data any
}

// render はページをレイアウトに入れて描画して返す。
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
	t, ok := h.pages[page]
	if !ok {
		h.log.Error("unknown page template", "page", page)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	v := view{Title: title, Version: h.version, Data: data}
	if a, ok := accountFrom(r.Context()); ok {
		v.User = &a
	}
	h.execute(w, r, status, t, "layout", v)
}

// renderBlock はページの中の 1 つのテンプレート（htmx で差し替える部分）だけを描画して返す。
func (h *Handler) renderBlock(w http.ResponseWriter, r *http.Request, status int, page, block string, data any) {
	t, ok := h.pages[page]
	if !ok {
		h.log.Error("unknown page template", "page", page)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.execute(w, r, status, t, block, data)
}

// execute はテンプレートを描画して返す。描画が終わってから書き出すので、途中で失敗しても壊れた HTML は返さない。
func (h *Handler) execute(w http.ResponseWriter, r *http.Request, status int, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		h.log.Error("render template", "template", name, "error", err)
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
	// Link と LinkLabel は案内するリンク。空ならホームへのリンクを出す。
	Link      string
	LinkLabel string
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.renderErrorLink(w, r, status, message, "", "")
}

// renderErrorLink はエラー画面に案内のリンクを付けて返す。
// 他の操作で対象が削除された場合などに、一覧を読み直せるようにする。
func (h *Handler) renderErrorLink(w http.ResponseWriter, r *http.Request, status int, message, link, linkLabel string) {
	data := errorData{Status: status, Message: message, Link: link, LinkLabel: linkLabel}
	if r.Header.Get("HX-Request") == "true" {
		// htmx の操作では、差し替え先に関わらず本文全体をエラーの表示に置き換える。
		w.Header().Set("HX-Retarget", "main")
		w.Header().Set("HX-Reswap", "innerHTML")
		h.renderBlock(w, r, status, "error", "content", view{Title: http.StatusText(status), Version: h.version, Data: data})
		return
	}
	h.render(w, r, status, "error", http.StatusText(status), data)
}
