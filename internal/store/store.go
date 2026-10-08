// Package store は BFF 専用の Valkey 上のアカウント・セッション・監査ログを扱う。
// キー設計は docs/design-overview.md の「データモデル」を参照。
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

var (
	// ErrNotFound は対象が存在しないことを表す。
	ErrNotFound = errors.New("not found")
	// ErrExists は同じユーザーID のアカウントが既に存在することを表す。
	ErrExists = errors.New("already exists")
)

// Store は Valkey への接続を持つ。
type Store struct {
	c valkey.Client
}

// Options は Valkey への接続設定。
type Options struct {
	Addr     string // host:port
	Password string
	// DB は使用する論理データベースの番号。通常は 0。
	DB int
}

// Open は Valkey に接続し、疎通を確認する。
func Open(ctx context.Context, o Options) (*Store, error) {
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{o.Addr},
		Password:    o.Password,
		SelectDB:    o.DB,
		ClientName:  "eapaka-webgui",
		// 単一ノード前提で、クライアント側キャッシュは使わない。
		DisableCache: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect valkey %s: %w", o.Addr, err)
	}
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		c.Close()
		return nil, fmt.Errorf("ping valkey %s: %w", o.Addr, err)
	}
	return &Store{c: c}, nil
}

// Close は接続を閉じる。
func (s *Store) Close() { s.c.Close() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

func boolField(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
