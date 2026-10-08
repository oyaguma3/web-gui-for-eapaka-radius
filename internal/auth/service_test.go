package auth

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth/authtest"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
)

const (
	ownerID = "root"
	ownerPW = "owner-password-123"
)

func testOptions(st Store) Options {
	return Options{
		Store:                st,
		Log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		InitialAdminID:       ownerID,
		InitialAdminPassword: ownerPW,
		SessionIdleTimeout:   30 * time.Minute,
		SessionMaxAge:        12 * time.Hour,
		MaxLoginFailures:     3,
		LockDuration:         15 * time.Minute,
		AuditMaxLen:          1000,
	}
}

func newTestService(t *testing.T) (*Service, *authtest.MemStore) {
	t.Helper()
	st := authtest.NewMemStore()
	s, err := New(t.Context(), testOptions(st))
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func login(t *testing.T, s *Service, id, pw string) (string, Account) {
	t.Helper()
	token, acct, err := s.Login(t.Context(), id, pw)
	if err != nil {
		t.Fatalf("login %s: %v", id, err)
	}
	return token, acct
}

func TestNewValidatesInitialAdmin(t *testing.T) {
	for name, mod := range map[string]func(*Options){
		"bad id":         func(o *Options) { o.InitialAdminID = "root user" },
		"short password": func(o *Options) { o.InitialAdminPassword = "short" },
	} {
		o := testOptions(authtest.NewMemStore())
		mod(&o)
		if _, err := New(t.Context(), o); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// 同じユーザーID のアカウントが Valkey にあれば起動しない。
	st := authtest.NewMemStore()
	st.Users[ownerID] = store.User{ID: ownerID, Role: "admin"}
	if _, err := New(t.Context(), testOptions(st)); err == nil || !strings.Contains(err.Error(), "also registered") {
		t.Errorf("conflict: err = %v", err)
	}
}

func TestOwnerLoginAndStamp(t *testing.T) {
	s, st := newTestService(t)
	ctx := WithRemote(t.Context(), "100.64.0.2:5555")

	token, acct, err := s.Login(ctx, ownerID, ownerPW)
	if err != nil || acct.Role != RoleOwner || acct.MustChangePassword {
		t.Fatalf("login = %+v, %v", acct, err)
	}
	if e := st.LastAudit(); e.Action != "login.success" || e.Actor != ownerID || e.Remote != "100.64.0.2:5555" {
		t.Errorf("audit = %+v", e)
	}
	// Cookie に入れる値そのものは保存しない。
	if _, ok := st.Sessions[token]; ok {
		t.Error("raw token is stored as a key")
	}
	if got, err := s.Authenticate(t.Context(), token); err != nil || got.ID != ownerID {
		t.Errorf("authenticate = %+v, %v", got, err)
	}

	// 同じ設定で起動し直してもセッションは続く。
	s2, err := New(t.Context(), testOptions(st))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Authenticate(t.Context(), token); err != nil {
		t.Errorf("after restart: %v", err)
	}
	// .env のパスワードを変えて起動し直すと、セッションは無効になる。
	o := testOptions(st)
	o.InitialAdminPassword = "changed-password-456"
	s3, err := New(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Authenticate(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after password change: err = %v", err)
	}
}

func TestLoginFailuresAndLock(t *testing.T) {
	s, st := newTestService(t)
	ctx := t.Context()

	for i := range 3 {
		if _, _, err := s.Login(ctx, ownerID, "wrong-password-xx"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: err = %v", i, err)
		}
	}
	if e := st.LastAudit(); e.Action != "login.failure" || !strings.Contains(e.Detail, `"locked":true`) {
		t.Errorf("audit = %+v", e)
	}
	auditCount := len(st.Audit)
	// ロック中は正しいパスワードでも入れない。監査ログにも追記しない。
	if _, _, err := s.Login(ctx, ownerID, ownerPW); !errors.Is(err, ErrLocked) {
		t.Errorf("locked: err = %v", err)
	}
	if len(st.Audit) != auditCount {
		t.Error("locked attempt was written to the audit log")
	}
	// 期限が過ぎれば（ここでは回数を消して代用）入れる。成功すると回数は消える。
	st.Failures[ownerID] = 2
	login(t, s, ownerID, ownerPW)
	if st.Failures[ownerID] != 0 {
		t.Errorf("failures after success = %d", st.Failures[ownerID])
	}

	// 存在しない ID も同じように数え、同じエラーを返す。
	if _, _, err := s.Login(ctx, "ghost", "whatever-password"); !errors.Is(err, ErrInvalidCredentials) || st.Failures["ghost"] != 1 {
		t.Errorf("unknown id: err = %v, failures = %d", err, st.Failures["ghost"])
	}
	// 形式の合わない ID は数えない。
	if _, _, err := s.Login(ctx, "bad id", "whatever-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("bad id: err = %v", err)
	}
	if _, ok := st.Failures["bad id"]; ok {
		t.Error("bad id was counted")
	}
}

func TestAuthenticate(t *testing.T) {
	s, st := newTestService(t)

	for _, bad := range []string{"", "short", strings.Repeat("A", 43)} {
		if _, err := s.Authenticate(t.Context(), bad); !errors.Is(err, ErrNoSession) {
			t.Errorf("token %q: err = %v", bad, err)
		}
	}

	token, _ := login(t, s, ownerID, ownerPW)
	// ログインから最大時間が過ぎたら無効にして消す。
	s.now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	if _, err := s.Authenticate(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Errorf("max age: err = %v", err)
	}
	if len(st.Sessions) != 0 {
		t.Error("expired session was not deleted")
	}
	s.now = time.Now

	token, _ = login(t, s, ownerID, ownerPW)
	if err := s.Logout(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(t.Context(), token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after logout: err = %v", err)
	}
}

func TestAccountPermissions(t *testing.T) {
	s, st := newTestService(t)
	ctx := t.Context()
	owner := s.ownerAccount()

	if err := s.CreateAccount(ctx, owner, "alice", RoleAdmin, "alice-password-1"); err != nil {
		t.Fatal(err)
	}
	if u := st.Users["alice"]; u.Role != "admin" || !u.MustChangePassword || u.CreatedBy != ownerID {
		t.Errorf("alice = %+v", u)
	}
	if e := st.LastAudit(); e.Action != "account.create" || e.Actor != ownerID || e.Target != "alice" {
		t.Errorf("audit = %+v", e)
	}
	alice, _ := s.lookup(ctx, "alice")
	admin := alice.account

	// 管理者は一般ユーザーだけを作れる。
	if err := s.CreateAccount(ctx, admin, "bob", RoleUser, "bob-password-12"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAccount(ctx, admin, "carol", RoleAdmin, "carol-password-1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin creates admin: err = %v", err)
	}
	bobCred, _ := s.lookup(ctx, "bob")
	user := bobCred.account
	if err := s.CreateAccount(ctx, user, "dave", RoleUser, "dave-password-12"); !errors.Is(err, ErrForbidden) {
		t.Errorf("user creates user: err = %v", err)
	}

	// 入力の誤り。
	for name, tc := range map[string]struct {
		id, pw string
		role   Role
		want   error
	}{
		"duplicate":   {"bob", "bob-password-12", RoleUser, ErrExists},
		"owner id":    {ownerID, "some-password-12", RoleUser, ErrExists},
		"bad id":      {"b ob", "some-password-12", RoleUser, nil},
		"short":       {"erin", "short", RoleUser, nil},
		"7 chars":     {"erin", "1234567", RoleUser, nil},
		"same as id":  {"frank.example", "FRANK.EXAMPLE", RoleUser, nil},
		"owner role":  {"gina", "some-password-12", RoleOwner, nil},
		"unknown rol": {"hana", "some-password-12", Role("root"), nil},
	} {
		err := s.CreateAccount(ctx, owner, tc.id, tc.role, tc.pw)
		if tc.want != nil {
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if _, ok := errors.AsType[*InputError](err); !ok {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// 8 文字ちょうどは使える。
	if err := s.CreateAccount(ctx, owner, "ivy", RoleUser, "12345678"); err != nil {
		t.Errorf("8 chars: %v", err)
	}
	if err := s.DeleteAccount(ctx, owner, "ivy"); err != nil {
		t.Fatal(err)
	}

	// 一覧は管理者だけ。最初の管理者が先頭。
	if _, err := s.ListAccounts(ctx, user); !errors.Is(err, ErrForbidden) {
		t.Errorf("user lists: err = %v", err)
	}
	list, err := s.ListAccounts(ctx, admin)
	if err != nil || len(list) != 3 || list[0].ID != ownerID || list[0].Role != RoleOwner {
		t.Errorf("list = %+v, %v", list, err)
	}

	// 削除: 管理者は管理者を消せない。最初の管理者は誰も消せない。
	if err := s.DeleteAccount(ctx, admin, "alice"); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin deletes admin: err = %v", err)
	}
	if err := s.DeleteAccount(ctx, owner, ownerID); !errors.Is(err, ErrForbidden) {
		t.Errorf("delete owner: err = %v", err)
	}
	if err := s.DeleteAccount(ctx, owner, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: err = %v", err)
	}

	// 削除したアカウントのセッションは無効になる。
	bobToken, _ := login(t, s, "bob", "bob-password-12")
	if err := s.DeleteAccount(ctx, admin, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, bobToken); !errors.Is(err, ErrNoSession) {
		t.Errorf("deleted user's session: err = %v", err)
	}
	if err := s.DeleteAccount(ctx, owner, "alice"); err != nil {
		t.Errorf("owner deletes admin: %v", err)
	}
}

func TestPasswordResetAndChange(t *testing.T) {
	s, st := newTestService(t)
	ctx := t.Context()
	owner := s.ownerAccount()
	if err := s.CreateAccount(ctx, owner, "bob", RoleUser, "bob-password-12"); err != nil {
		t.Fatal(err)
	}

	tokenA, bob := login(t, s, "bob", "bob-password-12")
	if !bob.MustChangePassword {
		t.Error("new account must change password")
	}

	// 自分のパスワード変更。
	if _, err := s.ChangePassword(ctx, bob, tokenA, "wrong-current-pw", "bob-new-password"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong current: err = %v", err)
	}
	if _, err := s.ChangePassword(ctx, bob, tokenA, "bob-password-12", "bob-password-12"); err == nil {
		t.Error("same password: want error")
	}
	tokenB, _ := login(t, s, "bob", "bob-password-12") // 別の端末のセッション
	newToken, err := s.ChangePassword(ctx, bob, tokenA, "bob-password-12", "bob-new-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tokenA); !errors.Is(err, ErrNoSession) {
		t.Errorf("old token: err = %v", err)
	}
	if _, err := s.Authenticate(ctx, tokenB); !errors.Is(err, ErrNoSession) {
		t.Errorf("other session: err = %v", err)
	}
	got, err := s.Authenticate(ctx, newToken)
	if err != nil || got.MustChangePassword {
		t.Errorf("new token: %+v, %v", got, err)
	}
	if e := st.LastAudit(); e.Action != "account.password.change" || e.Actor != "bob" {
		t.Errorf("audit = %+v", e)
	}

	// 最初の管理者は画面から変えられない。
	ownerToken, _ := login(t, s, ownerID, ownerPW)
	if _, err := s.ChangePassword(ctx, owner, ownerToken, ownerPW, "new-owner-password"); !errors.Is(err, ErrForbidden) {
		t.Errorf("owner change: err = %v", err)
	}

	// 再設定: セッションは無効になり、次のログインで変更を求める。
	if err := s.ResetPassword(ctx, owner, "bob", "bob-reset-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, newToken); !errors.Is(err, ErrNoSession) {
		t.Errorf("after reset: err = %v", err)
	}
	if _, acct := login(t, s, "bob", "bob-reset-password"); !acct.MustChangePassword {
		t.Error("after reset: must change password")
	}
	if e := st.LastAudit(); e.Action != "login.success" {
		t.Errorf("audit = %+v", e)
	}
	if err := s.ResetPassword(ctx, owner, ownerID, "whatever-password"); !errors.Is(err, ErrForbidden) {
		t.Errorf("reset owner: err = %v", err)
	}
}

func TestPasswordHash(t *testing.T) {
	ctx := t.Context()
	h, err := hashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash = %q", h)
	}
	if ok, err := verifyPassword(ctx, "correct horse battery", h); !ok || err != nil {
		t.Errorf("verify correct = %v, %v", ok, err)
	}
	if ok, _ := verifyPassword(ctx, "correct horse batterY", h); ok {
		t.Error("verify wrong = true")
	}
	h2, _ := hashPassword(ctx, "correct horse battery")
	if h == h2 {
		t.Error("salt is not random")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=65536,t=3,p=4$AAAA$AAAA", "$argon2id$v=19$m=999999999,t=3,p=4$AAAA$AAAA"} {
		if _, err := verifyPassword(ctx, "x", bad); err == nil {
			t.Errorf("verify %q: want error", bad)
		}
	}
}
