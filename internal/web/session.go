package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// sessionCookie はセッションID を入れる Cookie の名前。
// __Host- を付けると、ブラウザは Secure・Path=/・Domain なしの場合だけ受け付ける。
// Cookie はポートを区別しないので、同じホストで動かす aka 版（__Host-aka-webgui-session）とは別の名前にする。
const sessionCookie = "__Host-eapaka-webgui-session"

// maxFormBytes はフォームのボディの上限。
const maxFormBytes = 64 << 10

func setSessionCookie(w http.ResponseWriter, token string) {
	// 有効期限は付けない（ブラウザを閉じると消える）。有効期間はサーバー側のセッションで管理する。
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

type accountKey struct{}

// accountFrom はログイン中のアカウントを返す。
func accountFrom(ctx context.Context) (auth.Account, bool) {
	a, ok := ctx.Value(accountKey{}).(auth.Account)
	return a, ok
}

// withRemote は、監査ログに残す操作元のアドレスをコンテキストに入れる。
// リバースプロキシを置かない構成なので、接続元のアドレスをそのまま使う。
func withRemote(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithRemote(r.Context(), r.RemoteAddr)))
	})
}

// authed はログインを必須にする。パスワードの変更を求められているアカウントは、パスワード変更の画面へ送る。
func (h *Handler) authed(fn http.HandlerFunc) http.Handler {
	return h.session(fn, false)
}

// authedForPasswordChange はログインを必須にする。パスワードの変更を求められていても通す。
func (h *Handler) authedForPasswordChange(fn http.HandlerFunc) http.Handler {
	return h.session(fn, true)
}

// adminOnly はログインを必須にし、管理者（最初の管理者を含む）以外は 403 を返す。
func (h *Handler) adminOnly(fn http.HandlerFunc) http.Handler {
	return h.authed(func(w http.ResponseWriter, r *http.Request) {
		if a, _ := accountFrom(r.Context()); !a.IsAdmin() {
			h.renderError(w, r, http.StatusForbidden, "この操作の権限がありません。")
			return
		}
		fn(w, r)
	})
}

func (h *Handler) session(fn http.HandlerFunc, allowMustChange bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct, err := h.auth.Authenticate(r.Context(), sessionToken(r))
		if errors.Is(err, auth.ErrNoSession) {
			clearSessionCookie(w)
			h.redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusUnauthorized)
			return
		}
		if err != nil {
			h.log.Error("authenticate", "error", err)
			h.renderError(w, r, http.StatusInternalServerError, "セッションの確認に失敗しました。しばらくしてからやり直してください。")
			return
		}
		if acct.MustChangePassword && !allowMustChange {
			h.redirect(w, r, "/password", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), accountKey{}, acct)
		// Provisioning API には操作者（X-Operator-Id）として渡す。
		ctx = provapi.WithOperator(ctx, acct.ID)
		fn(w, r.WithContext(ctx))
	})
}

// redirect は画面を移動させる。htmx のリクエストには HX-Redirect で移動先を伝える
// （htmx は応答のステータスに関わらずこのヘッダーで移動する）。
// 通常のリクエストは、GET なら 303 で移動させ、それ以外は status を返して移動先へのリンクを示す。
func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, to string, status int) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(status)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	// フォームの送信中にセッションが切れた場合など。
	h.render(w, r, status, "error", http.StatusText(status), errorData{
		Status: status, Message: "ログインし直してください。", Link: to, LinkLabel: "ログイン画面へ",
	})
}

// seeOther は操作の完了後に別の画面へ移動させる。htmx のリクエストには HX-Redirect、それ以外には 303 を返す。
func seeOther(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// safeNext は、ログイン後の移動先として安全なパス（同じサイトの絶対パス）を返す。
// 別のサイトへ移動させる値（//example.com など）は使わない。
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/login") {
		return "/"
	}
	return next
}

// parseForm はフォームを読む。大きすぎるボディは拒否する。
func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	return r.ParseForm()
}
