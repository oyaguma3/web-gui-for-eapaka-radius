package web

import (
	"errors"
	"net/http"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
)

// ---- ログイン・ログアウト ----

// loginData はログイン画面に渡す値。
type loginData struct {
	ID    string
	Next  string
	Error string
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	// ログイン済みならそのまま進める。
	if _, err := h.auth.Authenticate(r.Context(), sessionToken(r)); err == nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	h.render(w, r, http.StatusOK, "login", "ログイン", loginData{Next: safeNext(r.URL.Query().Get("next"))})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	id, password, next := r.PostForm.Get("id"), r.PostForm.Get("password"), safeNext(r.PostForm.Get("next"))
	data := loginData{ID: id, Next: next}

	token, acct, err := h.auth.Login(r.Context(), id, password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		data.Error = "ユーザーID またはパスワードが違います。"
		h.render(w, r, http.StatusUnauthorized, "login", "ログイン", data)
		return
	case errors.Is(err, auth.ErrLocked):
		data.Error = "ログインの失敗が続いたため、このユーザーID では一時的にログインできません。しばらくしてからやり直してください。"
		h.render(w, r, http.StatusTooManyRequests, "login", "ログイン", data)
		return
	case err != nil:
		h.log.Error("login", "error", err)
		data.Error = "ログインの処理に失敗しました。しばらくしてからやり直してください。"
		h.render(w, r, http.StatusInternalServerError, "login", "ログイン", data)
		return
	}
	setSessionCookie(w, token)
	if acct.MustChangePassword {
		next = "/password"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		h.log.Error("logout", "error", err)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- 自分のパスワード変更 ----

// passwordData はパスワード変更の画面に渡す値。
type passwordData struct {
	Required bool // 初回ログインや再設定の後で、変更が必須か
	Done     bool
	Error    string
}

func (h *Handler) passwordPage(w http.ResponseWriter, r *http.Request) {
	acct, _ := accountFrom(r.Context())
	if !acct.CanChangeOwnPassword() {
		h.renderError(w, r, http.StatusForbidden, "最初の管理者のパスワードは .env で変更してください。")
		return
	}
	h.render(w, r, http.StatusOK, "password", "パスワード変更", passwordData{
		Required: acct.MustChangePassword,
		Done:     r.URL.Query().Get("done") == "1",
	})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	acct, _ := accountFrom(r.Context())
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	data := passwordData{Required: acct.MustChangePassword}
	current, password := r.PostForm.Get("current"), r.PostForm.Get("password")
	if password != r.PostForm.Get("confirm") {
		data.Error = "新しいパスワードと確認用のパスワードが一致しません。"
		h.render(w, r, http.StatusBadRequest, "password", "パスワード変更", data)
		return
	}
	token, err := h.auth.ChangePassword(r.Context(), acct, sessionToken(r), current, password)
	if err != nil {
		status, msg := accountErrorMessage(err)
		if status == http.StatusInternalServerError {
			h.log.Error("change password", "error", err)
		}
		data.Error = msg
		h.render(w, r, status, "password", "パスワード変更", data)
		return
	}
	setSessionCookie(w, token)
	http.Redirect(w, r, "/password?done=1", http.StatusSeeOther)
}

// ---- アカウント管理 ----

// accountsData はアカウント管理の画面に渡す値。
type accountsData struct {
	Me       auth.Account
	Accounts []auth.Account
	// Roles は作成できるアカウントの種類。
	Roles []auth.Role
	// Message は直前の操作の結果。
	Message string
	Error   string
	// Form は作成フォームの入力（エラーのときに戻す。パスワードは戻さない）。
	Form struct {
		ID   string
		Role auth.Role
	}
}

func (h *Handler) accountsData(r *http.Request) (accountsData, error) {
	me, _ := accountFrom(r.Context())
	d := accountsData{Me: me}
	for _, role := range []auth.Role{auth.RoleUser, auth.RoleAdmin} {
		if me.CanManage(role) {
			d.Roles = append(d.Roles, role)
		}
	}
	d.Form.Role = auth.RoleUser
	var err error
	d.Accounts, err = h.auth.ListAccounts(r.Context(), me)
	return d, err
}

func (h *Handler) accountsPage(w http.ResponseWriter, r *http.Request) {
	d, err := h.accountsData(r)
	if err != nil {
		h.log.Error("list accounts", "error", err)
		h.renderError(w, r, http.StatusInternalServerError, "アカウントの一覧を取得できませんでした。")
		return
	}
	h.render(w, r, http.StatusOK, "accounts", "アカウント管理", d)
}

// renderAccounts は操作の結果をアカウント管理の画面（htmx では一覧と作成フォームの部分だけ）で返す。
func (h *Handler) renderAccounts(w http.ResponseWriter, r *http.Request, status int, mutate func(*accountsData)) {
	d, err := h.accountsData(r)
	if err != nil {
		h.log.Error("list accounts", "error", err)
		h.renderError(w, r, http.StatusInternalServerError, "アカウントの一覧を取得できませんでした。")
		return
	}
	mutate(&d)
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "accounts", "accounts-section", d)
		return
	}
	h.render(w, r, status, "accounts", "アカウント管理", d)
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	me, _ := accountFrom(r.Context())
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	id, role, password := r.PostForm.Get("id"), auth.Role(r.PostForm.Get("role")), r.PostForm.Get("password")
	keepForm := func(d *accountsData) { d.Form.ID, d.Form.Role = id, role }
	if password != r.PostForm.Get("confirm") {
		h.renderAccounts(w, r, http.StatusBadRequest, func(d *accountsData) {
			keepForm(d)
			d.Error = "パスワードと確認用のパスワードが一致しません。"
		})
		return
	}
	if err := h.auth.CreateAccount(r.Context(), me, id, role, password); err != nil {
		status, msg := accountErrorMessage(err)
		if status == http.StatusInternalServerError {
			h.log.Error("create account", "error", err)
		}
		h.renderAccounts(w, r, status, func(d *accountsData) { keepForm(d); d.Error = msg })
		return
	}
	h.renderAccounts(w, r, http.StatusOK, func(d *accountsData) {
		d.Message = "アカウント " + id + " を作成しました。最初のログインでパスワードの変更を求めます。"
	})
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	me, _ := accountFrom(r.Context())
	id := r.PathValue("id")
	if err := h.auth.DeleteAccount(r.Context(), me, id); err != nil {
		status, msg := accountErrorMessage(err)
		if status == http.StatusInternalServerError {
			h.log.Error("delete account", "error", err)
		}
		h.renderAccounts(w, r, status, func(d *accountsData) { d.Error = msg })
		return
	}
	h.renderAccounts(w, r, http.StatusOK, func(d *accountsData) { d.Message = "アカウント " + id + " を削除しました。" })
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	me, _ := accountFrom(r.Context())
	id := r.PathValue("id")
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	password := r.PostForm.Get("password")
	if password != r.PostForm.Get("confirm") {
		h.renderAccounts(w, r, http.StatusBadRequest, func(d *accountsData) {
			d.Error = id + ": パスワードと確認用のパスワードが一致しません。"
		})
		return
	}
	if err := h.auth.ResetPassword(r.Context(), me, id, password); err != nil {
		status, msg := accountErrorMessage(err)
		if status == http.StatusInternalServerError {
			h.log.Error("reset password", "error", err)
		}
		h.renderAccounts(w, r, status, func(d *accountsData) { d.Error = id + ": " + msg })
		return
	}
	h.renderAccounts(w, r, http.StatusOK, func(d *accountsData) {
		d.Message = id + " のパスワードを再設定しました。次のログインでパスワードの変更を求めます。"
	})
}

// accountErrorMessage はアカウント操作のエラーを、ステータスと利用者向けの説明にする。
func accountErrorMessage(err error) (int, string) {
	if ie, ok := errors.AsType[*auth.InputError](err); ok {
		return http.StatusBadRequest, ie.Message
	}
	switch {
	case errors.Is(err, auth.ErrForbidden):
		return http.StatusForbidden, "この操作の権限がありません。"
	case errors.Is(err, auth.ErrNotFound):
		return http.StatusNotFound, "アカウントが見つかりません。"
	case errors.Is(err, auth.ErrExists):
		return http.StatusConflict, "そのユーザーID は既に使われています。"
	case errors.Is(err, auth.ErrWrongPassword):
		return http.StatusBadRequest, "現在のパスワードが違います。"
	}
	return http.StatusInternalServerError, "処理に失敗しました。しばらくしてからやり直してください。"
}
