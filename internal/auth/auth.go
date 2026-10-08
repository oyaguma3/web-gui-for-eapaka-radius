// Package auth は BFF のアカウント、権限、ログインとセッションを扱う。
//
// アカウントの種類:
//   - 最初の管理者（RoleOwner）: .env で指定する。Valkey には保存せず、削除もパスワード変更もできない。
//   - 管理者（RoleAdmin）: 最初の管理者だけが作成・削除できる。
//   - 一般ユーザー（RoleUser）: 管理者が作成・削除できる。
//
// 権限判定は BFF だけで行う。provisioning-api は BFF を全権の管理クライアントとして扱う。
package auth

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// Role はアカウントの種類。
type Role string

const (
	RoleOwner Role = "owner" // 最初の管理者
	RoleAdmin Role = "admin" // 管理者
	RoleUser  Role = "user"  // 一般ユーザー
)

// Label は画面に出す名前。
func (r Role) Label() string {
	switch r {
	case RoleOwner:
		return "最初の管理者"
	case RoleAdmin:
		return "管理者"
	case RoleUser:
		return "一般ユーザー"
	}
	return string(r)
}

// UserIDPattern はユーザーID の形式。管理API の X-Operator-Id にそのまま渡せる形にする。
var UserIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Account はアカウント。
type Account struct {
	ID   string
	Role Role
	// MustChangePassword は、パスワードを変更するまで他の画面を使わせないか。
	MustChangePassword bool
	CreatedAt          time.Time
	CreatedBy          string
}

// IsOwner は最初の管理者かを返す。
func (a Account) IsOwner() bool { return a.Role == RoleOwner }

// IsAdmin は管理者（最初の管理者を含む）かを返す。
func (a Account) IsAdmin() bool { return a.Role == RoleOwner || a.Role == RoleAdmin }

// CanManage は、role のアカウントを作成・削除し、パスワードを再設定できるかを返す。
func (a Account) CanManage(role Role) bool {
	switch a.Role {
	case RoleOwner:
		return role == RoleAdmin || role == RoleUser
	case RoleAdmin:
		return role == RoleUser
	}
	return false
}

// CanChangeOwnPassword は、画面から自分のパスワードを変更できるかを返す。
// 最初の管理者のパスワードは .env で変える。
func (a Account) CanChangeOwnPassword() bool { return a.Role != RoleOwner }

var (
	// ErrInvalidCredentials はユーザーID またはパスワードが違うことを表す。
	ErrInvalidCredentials = errors.New("invalid user id or password")
	// ErrLocked はログインの失敗が続いたため、一時的にログインできないことを表す。
	ErrLocked = errors.New("login is temporarily locked")
	// ErrNoSession はセッションがない、または無効になったことを表す。
	ErrNoSession = errors.New("no valid session")
	// ErrForbidden は権限がないことを表す。
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound はアカウントが存在しないことを表す。
	ErrNotFound = errors.New("account not found")
	// ErrExists は同じユーザーID のアカウントが既に存在することを表す。
	ErrExists = errors.New("account already exists")
	// ErrWrongPassword はパスワードの変更で、現在のパスワードが違うことを表す。
	ErrWrongPassword = errors.New("current password is wrong")
)

// InputError は入力の誤り。Message は利用者向けの説明。
type InputError struct {
	Field   string
	Message string
}

func (e *InputError) Error() string { return e.Field + ": " + e.Message }

// ---- コンテキスト ----

type remoteKey struct{}

// WithRemote は操作元のアドレスをコンテキストに入れる。監査ログに残す。
func WithRemote(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, remoteKey{}, addr)
}

func remoteFrom(ctx context.Context) string {
	v, _ := ctx.Value(remoteKey{}).(string)
	return v
}
