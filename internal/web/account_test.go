package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func newAccountEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnv(t, newFakeProv())
}

func TestLoginRequired(t *testing.T) {
	env := newAccountEnv(t)

	// 通常のリクエストはログイン画面へ。元のパスを next に入れる。
	w := do(env.h, request("GET", "/accounts?x=1", nil, nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2Faccounts%3Fx%3D1" {
		t.Errorf("GET: %d %q", w.Code, w.Header().Get("Location"))
	}
	// htmx のリクエストは HX-Redirect で移動させる。
	r := request("POST", "/accounts", nil, url.Values{})
	r.Header.Set("HX-Request", "true")
	w = do(env.h, r)
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("HX-Redirect"), "/login?next=") {
		t.Errorf("htmx: %d %q", w.Code, w.Header().Get("HX-Redirect"))
	}
	// 壊れた Cookie も未ログインとして扱い、Cookie を消す。
	w = do(env.h, request("GET", "/", &http.Cookie{Name: sessionCookie, Value: "garbage"}, nil))
	if w.Code != http.StatusSeeOther {
		t.Errorf("bad cookie: %d", w.Code)
	}
	if c := sessionCookieOf(w); c == nil || c.MaxAge >= 0 {
		t.Errorf("bad cookie was not cleared: %v", c)
	}
}

func TestLogin(t *testing.T) {
	env := newAccountEnv(t)

	w := do(env.h, request("GET", "/login?next=/accounts", nil, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `name="next" value="/accounts"`) {
		t.Errorf("login page: %d", w.Code)
	}

	w = do(env.h, request("POST", "/login", nil, url.Values{"id": {ownerID}, "password": {"wrong-password-1"}}))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "ユーザーID またはパスワードが違います。") ||
		sessionCookieOf(w) != nil {
		t.Errorf("wrong password: %d", w.Code)
	}
	// 入力したユーザーID は残す。
	if !strings.Contains(w.Body.String(), `value="root"`) {
		t.Error("user id was not kept")
	}

	w = do(env.h, request("POST", "/login", nil, url.Values{"id": {ownerID}, "password": {ownerPW}, "next": {"/accounts"}}))
	c := sessionCookieOf(w)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/accounts" || c == nil {
		t.Fatalf("login: %d %q", w.Code, w.Header().Get("Location"))
	}
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.MaxAge != 0 {
		t.Errorf("cookie attributes = %+v", c)
	}
	if !strings.HasPrefix(c.Name, "__Host-") {
		t.Errorf("cookie name = %q", c.Name)
	}

	// ログイン済みでログイン画面を開くと、そのまま進む。
	w = do(env.h, request("GET", "/login", c, nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("logged-in login page: %d %q", w.Code, w.Header().Get("Location"))
	}

	// ナビゲーションにユーザーID とログアウトが出る。
	body := do(env.h, request("GET", "/", c, nil)).Body.String()
	for _, want := range []string{"<summary>root</summary>", "最初の管理者", `action="/logout"`, `href="/accounts"`} {
		if !strings.Contains(body, want) {
			t.Errorf("nav does not contain %q", want)
		}
	}
	// 最初の管理者にはパスワード変更のリンクを出さない。
	if strings.Contains(body, `href="/password"`) {
		t.Error("owner sees password link")
	}

	// ログアウトすると、その Cookie はもう使えない。
	w = do(env.h, request("POST", "/logout", c, url.Values{}))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" || sessionCookieOf(w).MaxAge >= 0 {
		t.Errorf("logout: %d", w.Code)
	}
	if w := do(env.h, request("GET", "/", c, nil)); w.Code != http.StatusSeeOther {
		t.Errorf("after logout: %d", w.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/accounts?x=1":         "/accounts?x=1",
		"":                      "/",
		"https://evil.example/": "/",
		"//evil.example/":       "/",
		"/\\evil.example":       "/",
		"/login?next=/":         "/",
		"accounts":              "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountsFlow(t *testing.T) {
	env := newAccountEnv(t)
	owner := env.loginAs(t, ownerID, ownerPW)

	htmx := func(path string, c *http.Cookie, form url.Values) *http.Request {
		r := request("POST", path, c, form)
		r.Header.Set("HX-Request", "true")
		return r
	}

	// 作成（htmx では一覧の部分だけを返す）。
	w := do(env.h, htmx("/accounts", owner, url.Values{
		"id": {"alice"}, "role": {"admin"}, "password": {"alice-initial-pw"}, "confirm": {"alice-initial-pw"},
	}))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "アカウント alice を作成しました。") ||
		!strings.HasPrefix(body, `<div id="accounts">`) || strings.Contains(body, "<html") {
		t.Fatalf("create: %d %s", w.Code, body)
	}
	// 確認用のパスワードが違う。入力した ID は残し、パスワードは残さない。
	w = do(env.h, htmx("/accounts", owner, url.Values{
		"id": {"bob"}, "role": {"user"}, "password": {"bob-initial-pass"}, "confirm": {"bob-initial-pasS"},
	}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "一致しません") ||
		!strings.Contains(w.Body.String(), `value="bob"`) || strings.Contains(w.Body.String(), "bob-initial-pass") {
		t.Errorf("mismatch: %d %s", w.Code, w.Body.String())
	}
	// 規則に合わないパスワード。
	w = do(env.h, htmx("/accounts", owner, url.Values{"id": {"bob"}, "role": {"user"}, "password": {"short"}, "confirm": {"short"}}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "8 文字以上") {
		t.Errorf("weak: %d", w.Code)
	}

	// 作成されたアカウントは、最初のログインでパスワード変更へ送られる。
	alice := env.loginAs(t, "alice", "alice-initial-pw")
	if w := do(env.h, request("GET", "/", alice, nil)); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/password" {
		t.Errorf("must change: %d %q", w.Code, w.Header().Get("Location"))
	}
	w = do(env.h, request("GET", "/password", alice, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "続けるには、パスワードを変更してください。") {
		t.Errorf("password page: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `href="/accounts"`) {
		t.Error("nav links are shown while password change is required")
	}
	w = do(env.h, request("POST", "/password", alice, url.Values{
		"current": {"alice-initial-pw"}, "password": {"alice-own-password"}, "confirm": {"alice-own-password"},
	}))
	newAlice := sessionCookieOf(w)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/password?done=1" || newAlice == nil || newAlice.Value == alice.Value {
		t.Fatalf("change password: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(env.h, request("GET", "/", alice, nil)); w.Code != http.StatusSeeOther {
		t.Errorf("old cookie after change: %d", w.Code)
	}
	if w := do(env.h, request("GET", "/", newAlice, nil)); w.Code != http.StatusOK {
		t.Errorf("new cookie after change: %d", w.Code)
	}

	// 管理者は一般ユーザーだけを作れる。選択肢にも管理者を出さない。
	w = do(env.h, request("GET", "/accounts", newAlice, nil))
	if strings.Contains(w.Body.String(), `<option value="admin"`) || !strings.Contains(w.Body.String(), `<option value="user"`) {
		t.Errorf("admin's role options: %s", w.Body.String())
	}
	w = do(env.h, htmx("/accounts", newAlice, url.Values{"id": {"carol"}, "role": {"admin"}, "password": {"carol-initial-pw"}, "confirm": {"carol-initial-pw"}}))
	if w.Code != http.StatusForbidden {
		t.Errorf("admin creates admin: %d", w.Code)
	}
	w = do(env.h, htmx("/accounts", newAlice, url.Values{"id": {"bob"}, "role": {"user"}, "password": {"bob-initial-pass"}, "confirm": {"bob-initial-pass"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("admin creates user: %d", w.Code)
	}

	// 一般ユーザーはアカウント管理を使えない。
	bob := env.loginAs(t, "bob", "bob-initial-pass")
	w = do(env.h, request("POST", "/password", bob, url.Values{"current": {"bob-initial-pass"}, "password": {"bob-own-password"}, "confirm": {"bob-own-password"}}))
	bob = sessionCookieOf(w)
	if w := do(env.h, request("GET", "/accounts", bob, nil)); w.Code != http.StatusForbidden {
		t.Errorf("user opens accounts: %d", w.Code)
	}
	// htmx の操作で権限がなければ、本文全体をエラーの表示に置き換える（ページ全体は返さない）。
	w = do(env.h, htmx("/accounts/alice/delete", bob, url.Values{}))
	if w.Code != http.StatusForbidden || w.Header().Get("HX-Retarget") != "main" || w.Header().Get("HX-Reswap") != "innerHTML" ||
		strings.Contains(w.Body.String(), "<html") || !strings.Contains(w.Body.String(), "この操作の権限がありません。") {
		t.Errorf("user deletes: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	if strings.Contains(do(env.h, request("GET", "/", bob, nil)).Body.String(), `href="/accounts"`) {
		t.Error("user sees accounts link")
	}

	// 再設定: 対象のセッションは無効になる。
	w = do(env.h, htmx("/accounts/bob/password", newAlice, url.Values{"password": {"bob-reset-password"}, "confirm": {"bob-reset-password"}}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "bob のパスワードを再設定しました。") {
		t.Errorf("reset: %d %s", w.Code, w.Body.String())
	}
	if w := do(env.h, request("GET", "/", bob, nil)); w.Code != http.StatusSeeOther {
		t.Errorf("bob after reset: %d", w.Code)
	}

	// 削除: 管理者は管理者を消せない。最初の管理者は消せる。
	if w := do(env.h, htmx("/accounts/alice/delete", newAlice, url.Values{})); w.Code != http.StatusForbidden {
		t.Errorf("admin deletes admin: %d", w.Code)
	}
	if w := do(env.h, htmx("/accounts/nobody/delete", owner, url.Values{})); w.Code != http.StatusNotFound {
		t.Errorf("delete missing: %d", w.Code)
	}
	w = do(env.h, htmx("/accounts/alice/delete", owner, url.Values{}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "アカウント alice を削除しました。") {
		t.Errorf("owner deletes admin: %d", w.Code)
	}
	if w := do(env.h, request("GET", "/", newAlice, nil)); w.Code != http.StatusSeeOther {
		t.Errorf("deleted alice: %d", w.Code)
	}

	// 監査ログ（BFF）に操作者と操作元が残る。
	e := env.store.LastAudit()
	if e.Action != "account.delete" || e.Actor != ownerID || e.Target != "alice" || e.Remote == "" {
		t.Errorf("audit = %+v", e)
	}
}

func TestOwnerCannotChangePasswordOnScreen(t *testing.T) {
	env := newAccountEnv(t)
	owner := env.loginAs(t, ownerID, ownerPW)
	if w := do(env.h, request("GET", "/password", owner, nil)); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), ".env") {
		t.Errorf("owner password page: %d", w.Code)
	}
	w := do(env.h, request("POST", "/password", owner, url.Values{"current": {ownerPW}, "password": {"another-password1"}, "confirm": {"another-password1"}}))
	if w.Code != http.StatusForbidden {
		t.Errorf("owner change: %d", w.Code)
	}
}
