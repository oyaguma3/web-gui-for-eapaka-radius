package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/valkey-io/valkey-go"
)

const keyUsers = "users"

func userKey(id string) string { return "user:" + id }

// User は Valkey に保存するアカウント。最初の管理者（.env で指定）は含まない。
type User struct {
	ID   string
	Role string
	// PasswordHash は argon2id のハッシュ（PHC 形式）。
	PasswordHash string
	// Stamp はパスワードを変えるたびに変わる値。セッションに写しておき、一致しなければセッションを無効にする。
	Stamp string
	// MustChangePassword は、次のログインでパスワードの変更を求めるか。
	// 他人が設定したパスワード（作成時と再設定時）のときに立てる。
	MustChangePassword bool
	CreatedAt          time.Time
	CreatedBy          string
}

// createUserScript は、同じユーザーID がなければアカウントを作る。作ったら 1、既にあれば 0 を返す。
var createUserScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV, 2))
redis.call('SADD', KEYS[2], ARGV[1])
return 1
`)

// CreateUser はアカウントを作る。同じユーザーID があれば ErrExists を返す。
func (s *Store) CreateUser(ctx context.Context, u User) error {
	n, err := createUserScript.Exec(ctx, s.c, []string{userKey(u.ID), keyUsers}, []string{
		u.ID,
		"role", u.Role,
		"password_hash", u.PasswordHash,
		"stamp", u.Stamp,
		"must_change_password", boolField(u.MustChangePassword),
		"created_at", formatTime(u.CreatedAt),
		"created_by", u.CreatedBy,
	}).AsInt64()
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if n == 0 {
		return ErrExists
	}
	return nil
}

// GetUser はアカウントを返す。なければ ErrNotFound を返す。
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	m, err := s.c.Do(ctx, s.c.B().Hgetall().Key(userKey(id)).Build()).AsStrMap()
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	if len(m) == 0 {
		return User{}, ErrNotFound
	}
	return userFromMap(id, m), nil
}

func userFromMap(id string, m map[string]string) User {
	return User{
		ID:                 id,
		Role:               m["role"],
		PasswordHash:       m["password_hash"],
		Stamp:              m["stamp"],
		MustChangePassword: m["must_change_password"] == "1",
		CreatedAt:          parseTime(m["created_at"]),
		CreatedBy:          m["created_by"],
	}
}

// ListUsers は全アカウントをユーザーID の順で返す。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	ids, err := s.c.Do(ctx, s.c.B().Smembers().Key(keyUsers).Build()).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	cmds := make(valkey.Commands, len(ids))
	for i, id := range ids {
		cmds[i] = s.c.B().Hgetall().Key(userKey(id)).Build()
	}
	users := make([]User, 0, len(ids))
	for i, res := range s.c.DoMulti(ctx, cmds...) {
		m, err := res.AsStrMap()
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		// 索引だけが残っている場合（削除の途中など）は飛ばす。
		if len(m) > 0 {
			users = append(users, userFromMap(ids[i], m))
		}
	}
	slices.SortFunc(users, func(a, b User) int { return cmp.Compare(a.ID, b.ID) })
	return users, nil
}

// deleteUserScript はアカウントを削除する。削除したら 1、なければ 0 を返す。
var deleteUserScript = valkey.NewLuaScript(`
local n = redis.call('DEL', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[1])
return n
`)

// DeleteUser はアカウントを削除する。なければ ErrNotFound を返す。
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	n, err := deleteUserScript.Exec(ctx, s.c, []string{userKey(id), keyUsers}, []string{id}).AsInt64()
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// setPasswordScript は、アカウントがあればパスワードを置き換える。置き換えたら 1、なければ 0 を返す。
var setPasswordScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('HSET', KEYS[1], 'password_hash', ARGV[1], 'stamp', ARGV[2], 'must_change_password', ARGV[3])
return 1
`)

// SetPassword はパスワードのハッシュとスタンプを置き換える。なければ ErrNotFound を返す。
func (s *Store) SetPassword(ctx context.Context, id, passwordHash, stamp string, mustChange bool) error {
	n, err := setPasswordScript.Exec(ctx, s.c, []string{userKey(id)},
		[]string{passwordHash, stamp, boolField(mustChange)}).AsInt64()
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
