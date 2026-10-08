package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id のパラメーター。RFC 9106 の推奨（2 番目の候補: メモリ 64MiB、反復 3、並列度 4）に従う。
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// パスワードの長さの制限。
const (
	MinPasswordLen = 8   // 文字数
	MaxPasswordLen = 256 // バイト数
)

// hashSlots は同時に計算するハッシュの数を制限する。1 回で 64MiB を使うので、
// ログインの要求が集中してもメモリを使い切らないようにする。
var hashSlots = make(chan struct{}, 4)

func acquireSlot(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSlot() { <-hashSlots }

var b64 = base64.RawStdEncoding

// hashPassword は argon2id のハッシュを PHC 形式（$argon2id$v=19$m=...,t=...,p=...$salt$hash）で返す。
func hashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	rand.Read(salt)
	return hashWithSalt(ctx, password, salt)
}

func hashWithSalt(ctx context.Context, password string, salt []byte) (string, error) {
	if err := acquireSlot(ctx); err != nil {
		return "", err
	}
	defer releaseSlot()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// verifyPassword はパスワードがハッシュと一致するかを返す。
func verifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil ||
		memory == 0 || memory > 1024*1024 || time == 0 || time > 100 || threads == 0 {
		return false, errBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}

	if err := acquireSlot(ctx); err != nil {
		return false, err
	}
	defer releaseSlot()
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash は存在しないユーザーID でログインしようとしたときに照合するハッシュ。
// 存在するかどうかで応答時間が変わらないようにする。
var dummyHash = sync.OnceValue(func() string {
	h, err := hashPassword(context.Background(), "dummy password for timing equalization")
	if err != nil {
		panic(err)
	}
	return h
})

// checkPasswordPolicy はパスワードが規則に合うかを確かめる。合わなければ利用者向けの説明を返す。
func checkPasswordPolicy(userID, password string) string {
	switch {
	case !utf8.ValidString(password):
		return "パスワードに使えない文字が含まれています。"
	case utf8.RuneCountInString(password) < MinPasswordLen:
		return fmt.Sprintf("パスワードは %d 文字以上にしてください。", MinPasswordLen)
	case len(password) > MaxPasswordLen:
		return fmt.Sprintf("パスワードは %d バイト以下にしてください。", MaxPasswordLen)
	case strings.EqualFold(password, userID):
		return "ユーザーID と同じパスワードは使えません。"
	}
	return ""
}
