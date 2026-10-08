// Package trace はトレースID を扱う。
//
// BFF はブラウザのリクエストごとにトレースID を採番してアクセスログに出し、Provisioning API の呼び出しに
// X-Trace-ID ヘッダーで渡す。provisioning-api は受け取った値をログと監査ログの trace_id に使うので、
// BFF の操作と provisioning-api の記録をトレースID で突き合わせられる。
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type key struct{}

// New はトレースID（16進32桁。provisioning-api が自分で採番する形式と同じ）を作る。
func New() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// With はトレースID をコンテキストに入れる。
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From はコンテキストに入れたトレースID を返す。入っていなければ空文字列。
func From(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}
