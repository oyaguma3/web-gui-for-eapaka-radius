package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// セッションのキーには、Cookie に入れるセッションID そのものではなく、そのハッシュを使う（呼び出し側で計算する）。
func sessionKey(idHash string) string { return "session:" + idHash }

func loginFailKey(userID string) string { return "loginfail:" + userID }

// Session はログイン中のセッション。
type Session struct {
	UserID string
	// Stamp はログインした時点のアカウントのスタンプ。
	Stamp     string
	CreatedAt time.Time
}

// CreateSession はセッションを作る。ttl が過ぎると消える。
func (s *Store) CreateSession(ctx context.Context, idHash string, sess Session, ttl time.Duration) error {
	key := sessionKey(idHash)
	res := s.c.DoMulti(ctx,
		s.c.B().Multi().Build(),
		s.c.B().Hset().Key(key).FieldValue().
			FieldValue("user_id", sess.UserID).
			FieldValue("stamp", sess.Stamp).
			FieldValue("created_at", formatTime(sess.CreatedAt)).Build(),
		s.c.B().Pexpire().Key(key).Milliseconds(ttl.Milliseconds()).Build(),
		s.c.B().Exec().Build(),
	)
	for _, r := range res {
		if err := r.Error(); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}
	return nil
}

// GetSession はセッションを返し、有効期限を ttl だけ延ばす。ttl が 0 以下なら延ばさない。
// なければ ErrNotFound を返す。
func (s *Store) GetSession(ctx context.Context, idHash string, ttl time.Duration) (Session, error) {
	key := sessionKey(idHash)
	cmds := valkey.Commands{s.c.B().Hgetall().Key(key).Build()}
	if ttl > 0 {
		// 存在しないキーには何もしない。
		cmds = append(cmds, s.c.B().Pexpire().Key(key).Milliseconds(ttl.Milliseconds()).Xx().Build())
	}
	res := s.c.DoMulti(ctx, cmds...)
	m, err := res[0].AsStrMap()
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	if len(m) == 0 {
		return Session{}, ErrNotFound
	}
	return Session{UserID: m["user_id"], Stamp: m["stamp"], CreatedAt: parseTime(m["created_at"])}, nil
}

// DeleteSession はセッションを削除する。なくてもエラーにしない。
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	if err := s.c.Do(ctx, s.c.B().Del().Key(sessionKey(idHash)).Build()).Error(); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// LoginFailures はユーザーID ごとの直近のログイン失敗回数を返す。
func (s *Store) LoginFailures(ctx context.Context, userID string) (int64, error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(loginFailKey(userID)).Build()).ToString()
	if err != nil {
		if isNil(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("get login failures: %w", err)
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n, nil
}

// AddLoginFailure はログイン失敗回数を 1 増やし、増やした後の回数を返す。
// 回数は最初の失敗から window が過ぎると消える。
func (s *Store) AddLoginFailure(ctx context.Context, userID string, window time.Duration) (int64, error) {
	key := loginFailKey(userID)
	res := s.c.DoMulti(ctx,
		s.c.B().Incr().Key(key).Build(),
		// 期限は最初の失敗のときだけ設定する（失敗を続けても延びない）。
		s.c.B().Pexpire().Key(key).Milliseconds(window.Milliseconds()).Nx().Build(),
	)
	n, err := res[0].AsInt64()
	if err != nil {
		return 0, fmt.Errorf("add login failure: %w", err)
	}
	if err := res[1].Error(); err != nil {
		return 0, fmt.Errorf("add login failure: %w", err)
	}
	return n, nil
}

// ClearLoginFailures はログイン失敗回数を消す。
func (s *Store) ClearLoginFailures(ctx context.Context, userID string) error {
	if err := s.c.Do(ctx, s.c.B().Del().Key(loginFailKey(userID)).Build()).Error(); err != nil {
		return fmt.Errorf("clear login failures: %w", err)
	}
	return nil
}
