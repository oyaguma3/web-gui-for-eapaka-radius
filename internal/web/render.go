package web

import (
	"bytes"
	"fmt"
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
// 複数のページで使う部品は templates/partials/ に置く（すべてのページから使える）。
type pages map[string]*template.Template

// funcs はテンプレートで使う関数。
var funcs = template.FuncMap{
	// keysPlaceholder は、Ki / OPc をまだ表示していない状態の値を作る。
	"keysPlaceholder": func(imsi string, provisioner bool) keysData { return keysData{IMSI: imsi, Provisioner: provisioner} },
	// secretPlaceholder は、共有シークレットをまだ表示していない状態の値を作る。
	"secretPlaceholder": func(id int64) secretData { return secretData{ID: id} },
	// actionLabel は監査ログの操作の名前を返す。
	"actionLabel": actionLabel,
	// minPasswordLen はパスワードの最小文字数。入力欄の制限と説明に使う。
	"minPasswordLen": func() int { return auth.MinPasswordLen },
	// octets は通信量を単位つきで表示する。
	"octets": formatOctets,
	// issueLabel と issueHelp は provisioner の加入者の食い違い（issues）の名前と対処。
	"issueLabel": issueLabel,
	"issueHelp":  issueHelp,
	// keyStoreLabel は鍵の置き場所の名前。
	"keyStoreLabel": keyStoreLabel,
	// opStatusLabel、opKindLabel、stepLabel、stepStateLabel は操作の記録の状態・種類・手順の名前。
	"opStatusLabel":  opStatusLabel,
	"opKindLabel":    opKindLabel,
	"stepLabel":      stepLabel,
	"stepStateLabel": stepStateLabel,
	// downstreamLabel は provisioner の下流（prov / aka）の名前。
	"downstreamLabel": downstreamLabel,
	// detailText は監査ログの内容（JSON のオブジェクト）を 1 行の文字列にする。
	"detailText": detailText,
	// dict は「名前, 値, …」から部品に渡すマップを作る。add は整数の和。
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[fmt.Sprint(kv[i])] = kv[i+1]
		}
		return m
	},
	// list は引数を並べたスライス（選択肢を並べるのに使う）。deref は *bool の値（nil なら false）。
	"list":  func(v ...string) []string { return v },
	"deref": func(b *bool) bool { return b != nil && *b },
	"add": func(n ...int) int {
		sum := 0
		for _, v := range n {
			sum += v
		}
		return sum
	},
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
		ParseFS(assets, "templates/layout.html", "templates/partials/*.html")
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
	// Provisioner は eapaka-node-provisioner 経由でつないでいるか（メニューに「操作の記録」を出す）。
	Provisioner bool
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
	v := view{Title: title, Version: h.version, Data: data, Provisioner: h.pv != nil}
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
	// OperationID は provisioner の操作の記録の ID（あれば「操作の記録」へのリンクを出す）。
	OperationID string
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.renderErrorLink(w, r, status, message, "", "")
}

// renderErrorLink はエラー画面に案内のリンクを付けて返す。
// 他の操作で対象が削除された場合などに、一覧を読み直せるようにする。
func (h *Handler) renderErrorLink(w http.ResponseWriter, r *http.Request, status int, message, link, linkLabel string) {
	h.renderErrorData(w, r, errorData{Status: status, Message: message, Link: link, LinkLabel: linkLabel})
}

// renderFailure は接続先の呼び出しの失敗をエラー画面で返す。provisioner の操作の記録があれば、そこへのリンクも出す。
func (h *Handler) renderFailure(w http.ResponseWriter, r *http.Request, f apiFailure, link, linkLabel string) {
	h.renderErrorData(w, r, errorData{Status: f.Status, Message: f.Message, Link: link, LinkLabel: linkLabel, OperationID: f.OperationID})
}

func (h *Handler) renderErrorData(w http.ResponseWriter, r *http.Request, data errorData) {
	status := data.Status
	if r.Header.Get("HX-Request") == "true" {
		// htmx の操作では、差し替え先に関わらず本文全体をエラーの表示に置き換える。
		w.Header().Set("HX-Retarget", "main")
		w.Header().Set("HX-Reswap", "innerHTML")
		h.renderBlock(w, r, status, "error", "content", view{Title: http.StatusText(status), Version: h.version, Data: data, Provisioner: h.pv != nil})
		return
	}
	h.render(w, r, status, "error", http.StatusText(status), data)
}

// formatOctets は通信量（octets）を、単位をつけた読みやすい形にする（例: 512 B、1.5 KiB、20.0 MiB）。
func formatOctets(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + "KMGTP"[i:i+1] + "iB"
}
