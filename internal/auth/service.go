package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
)

// Store は Service が使うデータ操作。*store.Store が満たす。
type Store interface {
	CreateUser(ctx context.Context, u store.User) error
	GetUser(ctx context.Context, id string) (store.User, error)
	ListUsers(ctx context.Context) ([]store.User, error)
	DeleteUser(ctx context.Context, id string) error
	SetPassword(ctx context.Context, id, passwordHash, stamp string, mustChange bool) error

	CreateSession(ctx context.Context, idHash string, sess store.Session, ttl time.Duration) error
	GetSession(ctx context.Context, idHash string, ttl time.Duration) (store.Session, error)
	DeleteSession(ctx context.Context, idHash string) error

	LoginFailures(ctx context.Context, userID string) (int64, error)
	AddLoginFailure(ctx context.Context, userID string, window time.Duration) (int64, error)
	ClearLoginFailures(ctx context.Context, userID string) error

	AppendAudit(ctx context.Context, e store.AuditEntry, maxLen int64) error
	ListAudit(ctx context.Context, before string, limit int) ([]store.AuditEntry, string, error)
}

// Options は Service の設定。
type Options struct {
	Store Store
	Log   *slog.Logger

	// InitialAdminID と InitialAdminPassword は最初の管理者（.env で指定）。
	InitialAdminID       string
	InitialAdminPassword string

	// SessionIdleTimeout は、操作がないままこの時間が過ぎるとセッションを無効にする。
	SessionIdleTimeout time.Duration
	// SessionMaxAge は、操作を続けていてもログインからこの時間が過ぎるとセッションを無効にする。
	SessionMaxAge time.Duration

	// MaxLoginFailures 回続けて失敗すると、最初の失敗から LockDuration の間ログインできなくする。
	MaxLoginFailures int64
	LockDuration     time.Duration

	// AuditMaxLen は BFF の監査ログの保持件数の上限。
	AuditMaxLen int64
}

// Service はログイン、セッション、アカウントの操作を提供する。
type Service struct {
	opts  Options
	store Store
	log   *slog.Logger

	// owner は最初の管理者。パスワードのハッシュは起動時に計算してメモリにだけ持つ。
	owner struct {
		id, hash, stamp string
	}
	now func() time.Time
}

// New は Service を作る。最初の管理者の設定を確かめ、パスワードのハッシュを計算する。
func New(ctx context.Context, opts Options) (*Service, error) {
	s := &Service{opts: opts, store: opts.Store, log: opts.Log, now: time.Now}

	id, pw := opts.InitialAdminID, opts.InitialAdminPassword
	if !UserIDPattern.MatchString(id) {
		return nil, fmt.Errorf("initial admin id %q: must match %s", id, UserIDPattern)
	}
	if msg := checkPasswordPolicy(id, pw); msg != "" {
		return nil, fmt.Errorf("initial admin password: %s", msg)
	}
	// 最初の管理者のハッシュは、ユーザーID から決まるソルトで作る。起動し直しても同じ値になるので、
	// それから求めるスタンプも変わらず、パスワードを変えない限りセッションが続く。
	salt := sha256.Sum256([]byte("eapaka-webgui initial admin\x00" + id))
	hash, err := hashWithSalt(ctx, pw, salt[:argonSaltLen])
	if err != nil {
		return nil, err
	}
	stamp := sha256.Sum256([]byte(hash))
	s.owner.id, s.owner.hash, s.owner.stamp = id, hash, hex.EncodeToString(stamp[:16])

	// 最初の管理者と同じユーザーID のアカウントが Valkey にあると、どちらとして扱うかが曖昧になる。
	if _, err := s.store.GetUser(ctx, id); err == nil {
		return nil, fmt.Errorf("initial admin id %q is also registered as an account; delete it or change EAPAKA_WEBGUI_INITIAL_ADMIN_ID", id)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	// 存在しないユーザーID の照合に使うハッシュを、最初のログインより前に用意しておく。
	dummyHash()
	return s, nil
}

func (s *Service) ownerAccount() Account {
	return Account{ID: s.owner.id, Role: RoleOwner, CreatedBy: ".env"}
}

func accountFromUser(u store.User) Account {
	return Account{
		ID:                 u.ID,
		Role:               Role(u.Role),
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt,
		CreatedBy:          u.CreatedBy,
	}
}

// credential はログインの照合に使う値。
type credential struct {
	account Account
	hash    string
	stamp   string
}

// lookup はユーザーID から照合に使う値を引く。なければ store.ErrNotFound を返す。
func (s *Service) lookup(ctx context.Context, id string) (credential, error) {
	if id == s.owner.id {
		return credential{account: s.ownerAccount(), hash: s.owner.hash, stamp: s.owner.stamp}, nil
	}
	u, err := s.store.GetUser(ctx, id)
	if err != nil {
		return credential{}, err
	}
	return credential{account: accountFromUser(u), hash: u.PasswordHash, stamp: u.Stamp}, nil
}

// ---- ログインとセッション ----

// newToken はセッションID（Cookie に入れる値）と、Valkey のキーに使うそのハッシュを作る。
func newToken() (token, idHash string) {
	b := make([]byte, 32)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, tokenHash(token)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validToken はセッションID の形式（32 バイトの base64url）かを返す。
func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == 32
}

// Login はユーザーID とパスワードを照合し、セッションを作ってセッションID を返す。
func (s *Service) Login(ctx context.Context, id, password string) (string, Account, error) {
	if !UserIDPattern.MatchString(id) || len(password) > MaxPasswordLen {
		// 形式の合わない ID は保存先のキーに使わない。応答時間をそろえるために照合だけ行う。
		verifyPassword(ctx, password, dummyHash())
		return "", Account{}, ErrInvalidCredentials
	}

	failures, err := s.store.LoginFailures(ctx, id)
	if err != nil {
		return "", Account{}, err
	}
	if failures >= s.opts.MaxLoginFailures {
		// ロック中の試行は監査ログ（件数上限あり）には残さず、通常のログにだけ出す。
		s.log.Warn("login rejected: locked", "user_id", id, "remote", remoteFrom(ctx))
		return "", Account{}, ErrLocked
	}

	cred, err := s.lookup(ctx, id)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", Account{}, err
	}
	hash := cred.hash
	if !exists {
		hash = dummyHash()
	}
	ok, err := verifyPassword(ctx, password, hash)
	if err != nil {
		return "", Account{}, err
	}
	if !ok || !exists {
		n, err := s.store.AddLoginFailure(ctx, id, s.opts.LockDuration)
		if err != nil {
			return "", Account{}, err
		}
		s.audit(ctx, id, "login.failure", id, map[string]any{"failures": n, "locked": n >= s.opts.MaxLoginFailures})
		return "", Account{}, ErrInvalidCredentials
	}

	if err := s.store.ClearLoginFailures(ctx, id); err != nil {
		return "", Account{}, err
	}
	token, idHash := newToken()
	sess := store.Session{UserID: id, Stamp: cred.stamp, CreatedAt: s.now()}
	if err := s.store.CreateSession(ctx, idHash, sess, s.opts.SessionIdleTimeout); err != nil {
		return "", Account{}, err
	}
	s.audit(ctx, id, "login.success", id, nil)
	return token, cred.account, nil
}

// Authenticate はセッションID からアカウントを返し、セッションの有効期限を延ばす。
// セッションがない、期限切れ、アカウントが削除された、パスワードが変わった場合は ErrNoSession を返す。
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	if !validToken(token) {
		return Account{}, ErrNoSession
	}
	idHash := tokenHash(token)
	sess, err := s.store.GetSession(ctx, idHash, s.opts.SessionIdleTimeout)
	if errors.Is(err, store.ErrNotFound) {
		return Account{}, ErrNoSession
	}
	if err != nil {
		return Account{}, err
	}

	invalidate := func(reason string) (Account, error) {
		s.log.Info("session invalidated", "user_id", sess.UserID, "reason", reason)
		if err := s.store.DeleteSession(ctx, idHash); err != nil {
			return Account{}, err
		}
		return Account{}, ErrNoSession
	}
	if s.now().Sub(sess.CreatedAt) > s.opts.SessionMaxAge {
		return invalidate("max age exceeded")
	}
	cred, err := s.lookup(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return invalidate("account deleted")
	}
	if err != nil {
		return Account{}, err
	}
	if cred.stamp != sess.Stamp {
		return invalidate("password changed")
	}
	return cred.account, nil
}

// Logout はセッションを削除する。
func (s *Service) Logout(ctx context.Context, token string) error {
	if !validToken(token) {
		return nil
	}
	return s.store.DeleteSession(ctx, tokenHash(token))
}

// ---- アカウントの操作 ----

// ListAccounts はアカウントの一覧を返す。最初の管理者を先頭に置く。管理者だけが使える。
func (s *Service) ListAccounts(ctx context.Context, actor Account) ([]Account, error) {
	if !actor.IsAdmin() {
		return nil, ErrForbidden
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	accounts := []Account{s.ownerAccount()}
	for _, u := range users {
		accounts = append(accounts, accountFromUser(u))
	}
	return accounts, nil
}

// CreateAccount はアカウントを作る。次のログインでパスワードの変更を求める。
func (s *Service) CreateAccount(ctx context.Context, actor Account, id string, role Role, password string) error {
	if role != RoleAdmin && role != RoleUser {
		return &InputError{Field: "role", Message: "種類を選んでください。"}
	}
	if !actor.CanManage(role) {
		return ErrForbidden
	}
	if !UserIDPattern.MatchString(id) {
		return &InputError{Field: "id", Message: "ユーザーID は 1〜64 文字の英数字と . _ @ - で指定してください。"}
	}
	if id == s.owner.id {
		return ErrExists
	}
	if msg := checkPasswordPolicy(id, password); msg != "" {
		return &InputError{Field: "password", Message: msg}
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	err = s.store.CreateUser(ctx, store.User{
		ID: id, Role: string(role), PasswordHash: hash, Stamp: newStamp(),
		MustChangePassword: true, CreatedAt: s.now(), CreatedBy: actor.ID,
	})
	if errors.Is(err, store.ErrExists) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "account.create", id, map[string]any{"role": role})
	return nil
}

// target は操作対象のアカウントを引き、actor が操作できるかを確かめる。
func (s *Service) target(ctx context.Context, actor Account, id string) (Account, error) {
	if id == s.owner.id {
		return Account{}, ErrForbidden
	}
	u, err := s.store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a := accountFromUser(u)
	if !actor.CanManage(a.Role) {
		return Account{}, ErrForbidden
	}
	return a, nil
}

// DeleteAccount はアカウントを削除する。そのアカウントのセッションは次の操作で無効になる。
func (s *Service) DeleteAccount(ctx context.Context, actor Account, id string) error {
	a, err := s.target(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteUser(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "account.delete", id, map[string]any{"role": a.Role})
	return nil
}

// ResetPassword は他人のパスワードを再設定する。そのアカウントのセッションは無効になり、
// 次のログインでパスワードの変更を求める。
func (s *Service) ResetPassword(ctx context.Context, actor Account, id, password string) error {
	a, err := s.target(ctx, actor, id)
	if err != nil {
		return err
	}
	if msg := checkPasswordPolicy(id, password); msg != "" {
		return &InputError{Field: "password", Message: msg}
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return err
	}
	if err := s.store.SetPassword(ctx, id, hash, newStamp(), true); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	s.audit(ctx, actor.ID, "account.password.reset", id, map[string]any{"role": a.Role})
	return nil
}

// ChangePassword は自分のパスワードを変更する。他のセッションは無効になり、
// 現在のセッションは新しいセッションID に置き換える。新しいセッションID を返す。
func (s *Service) ChangePassword(ctx context.Context, actor Account, token, current, password string) (string, error) {
	if !actor.CanChangeOwnPassword() {
		return "", ErrForbidden
	}
	u, err := s.store.GetUser(ctx, actor.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	ok, err := verifyPassword(ctx, current, u.PasswordHash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrWrongPassword
	}
	if msg := checkPasswordPolicy(actor.ID, password); msg != "" {
		return "", &InputError{Field: "password", Message: msg}
	}
	if current == password {
		return "", &InputError{Field: "password", Message: "現在と同じパスワードは使えません。"}
	}
	hash, err := hashPassword(ctx, password)
	if err != nil {
		return "", err
	}
	stamp := newStamp()
	if err := s.store.SetPassword(ctx, actor.ID, hash, stamp, false); err != nil {
		return "", err
	}
	if err := s.Logout(ctx, token); err != nil {
		return "", err
	}
	newTok, idHash := newToken()
	if err := s.store.CreateSession(ctx, idHash, store.Session{UserID: actor.ID, Stamp: stamp, CreatedAt: s.now()},
		s.opts.SessionIdleTimeout); err != nil {
		return "", err
	}
	s.audit(ctx, actor.ID, "account.password.change", actor.ID, nil)
	return newTok, nil
}

func newStamp() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- 監査ログ ----

// ListAudit は BFF の監査ログを新しい順に返す。管理者だけが使える。
// before が空でなければ、そのエントリより古いものを返す。さらに古いものがあれば、次の before を返す。
func (s *Service) ListAudit(ctx context.Context, actor Account, before string, limit int) ([]store.AuditEntry, string, error) {
	if !actor.IsAdmin() {
		return nil, "", ErrForbidden
	}
	return s.store.ListAudit(ctx, before, limit)
}

// audit は BFF の監査ログを標準出力と Valkey の Stream に記録する。
// 記録に失敗しても操作自体は成功として扱い、エラーをログに残す。
func (s *Service) audit(ctx context.Context, actor, action, target string, detail map[string]any) {
	e := store.AuditEntry{Actor: actor, Action: action, Target: target, Remote: remoteFrom(ctx)}
	if detail != nil {
		b, err := json.Marshal(detail, json.Deterministic(true))
		if err != nil {
			s.log.Error("marshal audit detail", "action", action, "error", err)
		}
		e.Detail = string(b)
	}
	s.log.Info("audit", "actor", e.Actor, "action", action, "target", target, "remote", e.Remote, "detail", e.Detail)
	// リクエストが途中で切れても記録は残す。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.AppendAudit(ctx, e, s.opts.AuditMaxLen); err != nil {
		s.log.Error("append audit", "action", action, "target", target, "error", err)
	}
}
