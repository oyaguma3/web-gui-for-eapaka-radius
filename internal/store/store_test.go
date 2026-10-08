package store

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// 結合テストは実際の Valkey に接続する。接続先を環境変数で指定したときだけ実行する。
//
//	EAPAKA_WEBGUI_TEST_VALKEY_ADDR=127.0.0.1:16380 EAPAKA_WEBGUI_TEST_VALKEY_PASSWORD=... go test ./internal/store/
//
// テストは論理データベース 1 番の全データを消すので、専用の Valkey を使うこと。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("EAPAKA_WEBGUI_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("EAPAKA_WEBGUI_TEST_VALKEY_ADDR is not set")
	}
	s, err := Open(t.Context(), Options{Addr: addr, Password: os.Getenv("EAPAKA_WEBGUI_TEST_VALKEY_PASSWORD"), DB: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.c.Do(t.Context(), s.c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUsers(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	created := time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)

	bob := User{ID: "bob", Role: "user", PasswordHash: "h1", Stamp: "s1", MustChangePassword: true, CreatedAt: created, CreatedBy: "alice"}
	if err := s.CreateUser(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, User{ID: "alice", Role: "admin", PasswordHash: "h2", Stamp: "s2", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, bob); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: err = %v", err)
	}

	got, err := s.GetUser(ctx, "bob")
	if err != nil || got != bob {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := s.GetUser(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: err = %v", err)
	}

	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "alice" || list[1].ID != "bob" {
		t.Errorf("list = %+v, %v", list, err)
	}

	if err := s.SetPassword(ctx, "bob", "h3", "s3", false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(ctx, "bob")
	if got.PasswordHash != "h3" || got.Stamp != "s3" || got.MustChangePassword || got.Role != "user" {
		t.Errorf("after set password = %+v", got)
	}
	if err := s.SetPassword(ctx, "nobody", "h", "s", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("set password missing: err = %v", err)
	}
	// SetPassword は存在しないアカウントを作らない。
	if _, err := s.GetUser(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("set password created a user: err = %v", err)
	}

	if err := s.DeleteUser(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "bob"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: err = %v", err)
	}
	if list, _ := s.ListUsers(ctx); len(list) != 1 || list[0].ID != "alice" {
		t.Errorf("list after delete = %+v", list)
	}
}

func TestSessions(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	created := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.CreateSession(ctx, "h1", Session{UserID: "bob", Stamp: "s1", CreatedAt: created}, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "h1", 2*time.Minute)
	if err != nil || got.UserID != "bob" || got.Stamp != "s1" || !got.CreatedAt.Equal(created) {
		t.Errorf("get = %+v, %v", got, err)
	}
	// 取得すると有効期限が延びる。
	ttl, err := s.c.Do(ctx, s.c.B().Pttl().Key(sessionKey("h1")).Build()).AsInt64()
	if err != nil || ttl <= 60_000 {
		t.Errorf("ttl after get = %d, %v", ttl, err)
	}
	// ttl が 0 なら延ばさない。
	if _, err := s.GetSession(ctx, "h1", 0); err != nil {
		t.Fatal(err)
	}
	if ttl2, _ := s.c.Do(ctx, s.c.B().Pttl().Key(sessionKey("h1")).Build()).AsInt64(); ttl2 > ttl {
		t.Errorf("ttl after get without extension = %d (before %d)", ttl2, ttl)
	}
	if _, err := s.GetSession(ctx, "missing", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: err = %v", err)
	}
	// 存在しないセッションの取得で、キーを作らない。
	if n, _ := s.c.Do(ctx, s.c.B().Exists().Key(sessionKey("missing")).Build()).AsInt64(); n != 0 {
		t.Error("get created a session key")
	}

	if err := s.DeleteSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "h1", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v", err)
	}

	// 期限が過ぎると消える。
	if err := s.CreateSession(ctx, "h2", Session{UserID: "bob", CreatedAt: created}, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := s.GetSession(ctx, "h2", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after expiry: err = %v", err)
	}
}

func TestLoginFailures(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	if n, err := s.LoginFailures(ctx, "bob"); n != 0 || err != nil {
		t.Errorf("initial = %d, %v", n, err)
	}
	for want := range int64(3) {
		n, err := s.AddLoginFailure(ctx, "bob", time.Minute)
		if err != nil || n != want+1 {
			t.Errorf("add = %d, %v", n, err)
		}
	}
	if n, _ := s.LoginFailures(ctx, "bob"); n != 3 {
		t.Errorf("after add = %d", n)
	}
	// 期限は最初の失敗のときだけ設定する。
	ttl, _ := s.c.Do(ctx, s.c.B().Pttl().Key(loginFailKey("bob")).Build()).AsInt64()
	if ttl <= 0 || ttl > 60_000 {
		t.Errorf("ttl = %d", ttl)
	}
	if err := s.ClearLoginFailures(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.LoginFailures(ctx, "bob"); n != 0 {
		t.Errorf("after clear = %d", n)
	}
}

func TestAudit(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	for i := range 5 {
		if err := s.AppendAudit(ctx, AuditEntry{
			Actor: "alice", Action: "account.create", Target: fmt.Sprintf("u%d", i), Remote: "100.64.0.2:5000", Detail: `{"role":"user"}`,
		}, 1000); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := s.ListAudit(ctx, "", 3)
	if err != nil || len(page) != 3 || next == "" {
		t.Fatalf("page1 = %+v, %q, %v", page, next, err)
	}
	if page[0].Target != "u4" || page[0].Actor != "alice" || page[0].Remote != "100.64.0.2:5000" ||
		page[0].Detail != `{"role":"user"}` || time.Since(page[0].Time) > time.Minute {
		t.Errorf("newest = %+v", page[0])
	}
	page2, next2, err := s.ListAudit(ctx, next, 3)
	if err != nil || len(page2) != 2 || next2 != "" || page2[0].Target != "u1" || page2[1].Target != "u0" {
		t.Errorf("page2 = %+v, %q, %v", page2, next2, err)
	}
}
