// Package web はブラウザ向けの画面（HTML）と静的ファイルを返す。
//
// 同梱しているサードパーティのファイル（static/）:
//   - htmx.min.js: htmx 2.0.11（https://htmx.org、0BSD）
//   - pico.min.css: Pico CSS 2.1.1（https://picocss.com、MIT。著作権表示はファイル先頭）
//
// 更新するときは npm の配布物（jsdelivr）から同じファイル名で置き換え、上の版数も直す。
package web

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
)

//go:embed templates static
var assets embed.FS

// ProvAPI は画面が使う Provisioning API の操作。*provapi.Client が満たす。
// 将来 eapaka-node-provisioner に付け替えるときは、これを満たすクライアントを渡す。
type ProvAPI interface {
	Status(ctx context.Context) (provapi.Status, error)
}

// AuthService はログイン、セッション、アカウントの操作。*auth.Service が満たす。
type AuthService interface {
	Login(ctx context.Context, id, password string) (string, auth.Account, error)
	Authenticate(ctx context.Context, token string) (auth.Account, error)
	ListAudit(ctx context.Context, actor auth.Account, before string, limit int) ([]store.AuditEntry, string, error)
	Logout(ctx context.Context, token string) error
	ListAccounts(ctx context.Context, actor auth.Account) ([]auth.Account, error)
	CreateAccount(ctx context.Context, actor auth.Account, id string, role auth.Role, password string) error
	DeleteAccount(ctx context.Context, actor auth.Account, id string) error
	ResetPassword(ctx context.Context, actor auth.Account, id, password string) error
	ChangePassword(ctx context.Context, actor auth.Account, token, current, password string) (string, error)
}

// Handler は画面のハンドラー。
type Handler struct {
	log     *slog.Logger
	version string
	prov    ProvAPI
	auth    AuthService
	pages   pages
	static  *staticFiles
}

// Options は Handler の設定。
type Options struct {
	Log *slog.Logger
	// Version は画面のフッターに出すバージョン。
	Version string
	Prov    ProvAPI
	Auth    AuthService
}

// New は Handler を作る。テンプレートと静的ファイルはここで読み込む。
func New(opts Options) (*Handler, error) {
	h := &Handler{log: opts.Log, version: opts.Version, prov: opts.Prov, auth: opts.Auth}
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

	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.login)
	mux.HandleFunc("POST /logout", h.logout)
	mux.Handle("GET /password", h.authedForPasswordChange(h.passwordPage))
	mux.Handle("POST /password", h.authedForPasswordChange(h.changePassword))

	mux.Handle("GET /{$}", h.authed(h.dashboard))

	mux.Handle("GET /accounts", h.adminOnly(h.accountsPage))
	mux.Handle("POST /accounts", h.adminOnly(h.createAccount))
	mux.Handle("POST /accounts/{id}/delete", h.adminOnly(h.deleteAccount))
	mux.Handle("POST /accounts/{id}/password", h.adminOnly(h.resetPassword))

	mux.HandleFunc("/", h.notFound)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.log.Warn("cross-origin request rejected",
			"method", r.Method, "path", r.URL.Path, "origin", r.Header.Get("Origin"))
		h.renderError(w, r, http.StatusForbidden, "別のサイトからの操作は受け付けません。")
	}))

	var handler http.Handler = mux
	handler = cop.Handler(handler)
	handler = withRemote(handler)
	handler = securityHeaders(handler)
	handler = h.accessLog(handler)
	return handler
}

// dashboardData はダッシュボードに渡す値。
type dashboardData struct {
	Status provapi.Status
	// ProvError は Provisioning API から状態を取得できなかった場合の説明。
	ProvError string
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	var d dashboardData
	st, err := h.prov.Status(r.Context())
	if err != nil {
		h.log.Warn("get provisioning api status", "error", err)
		d.ProvError = provErrorMessage(err)
	}
	d.Status = st
	h.render(w, r, http.StatusOK, "dashboard", "ダッシュボード", d)
}

// provErrorMessage は、Provisioning API の呼び出しに失敗したときに画面に出す説明を返す。
func provErrorMessage(err error) string {
	if provapi.IsUnavailable(err) {
		if hint := provapi.Diagnose(err); hint != "" {
			return hint
		}
		return "本PoCの Provisioning API に接続できません。"
	}
	return "本PoCの Provisioning API がエラーを返しました。"
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusNotFound, "ページが見つかりません。")
}
