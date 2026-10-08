// Package config は環境変数から設定を読み込む。
package config

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config は BFF の設定。
type Config struct {
	// Addr はブラウザ向け HTTPS の待ち受けアドレス。
	Addr string
	// TLSCertFile と TLSKeyFile はブラウザ向けのサーバー証明書と秘密鍵（PEM）のパス。
	// どちらも存在しなければ、起動時に自己署名を生成してここに保存する。
	TLSCertFile string
	TLSKeyFile  string
	// TLSHosts は、自己署名のサーバー証明書を生成するときに SAN へ入れるホスト名と IP アドレス。
	TLSHosts []string

	// AdminURL は本PoCの Provisioning API（provisioning-api）のベース URL。
	AdminURL string
	// AdminClientCertFile は Provisioning API に提示するクライアント証明書（PEM）のパス。
	AdminClientCertFile string
	// AdminClientKeyFile はクライアント証明書の秘密鍵（PEM）のパス。空なら AdminClientCertFile から読む。
	AdminClientKeyFile string
	// AdminServerCertFile は provisioning-api のサーバー証明書（PEM）のパス。これを信頼する証明書として検証する。
	AdminServerCertFile string

	// ValkeyAddr は BFF 専用の Valkey の接続先（host:port）。
	ValkeyAddr string
	// ValkeyPassword は Valkey のパスワード。
	ValkeyPassword string

	// InitialAdminID と InitialAdminPassword は最初の管理者。.env を常に正とし、Valkey には保存しない。
	InitialAdminID       string
	InitialAdminPassword string

	// SessionIdleTimeout は、操作がないままこの時間が過ぎるとログアウトする時間。
	SessionIdleTimeout time.Duration
	// SessionMaxAge は、操作を続けていてもログインからこの時間が過ぎるとログアウトする時間。
	SessionMaxAge time.Duration
	// LoginMaxFailures 回続けてログインに失敗すると、LoginLockDuration の間そのユーザーID でログインできなくする。
	LoginMaxFailures  int64
	LoginLockDuration time.Duration

	// AuditMaxLen は BFF の監査ログの保持件数の上限。
	AuditMaxLen int64

	// LogLevel はログの出力レベル。
	LogLevel slog.Level
}

// Load は環境変数から設定を読み込む。
func Load() (Config, error) {
	c := Config{
		Addr:        cmp.Or(os.Getenv("EAPAKA_WEBGUI_ADDR"), ":8445"),
		TLSCertFile: cmp.Or(os.Getenv("EAPAKA_WEBGUI_TLS_CERT"), "/data/tls/cert.pem"),
		TLSKeyFile:  cmp.Or(os.Getenv("EAPAKA_WEBGUI_TLS_KEY"), "/data/tls/key.pem"),
		TLSHosts:    splitList(cmp.Or(os.Getenv("EAPAKA_WEBGUI_TLS_HOSTS"), "localhost,127.0.0.1")),

		AdminURL:            cmp.Or(os.Getenv("EAPAKA_WEBGUI_ADMIN_URL"), "https://provisioning-api:9444/admin/v1"),
		AdminClientCertFile: cmp.Or(os.Getenv("EAPAKA_WEBGUI_ADMIN_CLIENT_CERT"), "/certs/admin-client.pem"),
		AdminClientKeyFile:  os.Getenv("EAPAKA_WEBGUI_ADMIN_CLIENT_KEY"),
		AdminServerCertFile: cmp.Or(os.Getenv("EAPAKA_WEBGUI_ADMIN_SERVER_CERT"), "/certs/admin-server.pem"),

		ValkeyAddr:     cmp.Or(os.Getenv("EAPAKA_WEBGUI_VALKEY_ADDR"), "valkey:6379"),
		ValkeyPassword: os.Getenv("EAPAKA_WEBGUI_VALKEY_PASSWORD"),

		InitialAdminID:       os.Getenv("EAPAKA_WEBGUI_INITIAL_ADMIN_ID"),
		InitialAdminPassword: os.Getenv("EAPAKA_WEBGUI_INITIAL_ADMIN_PASSWORD"),
	}
	if v := os.Getenv("EAPAKA_WEBGUI_LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("EAPAKA_WEBGUI_LOG_LEVEL: %w", err)
		}
	}
	var err error
	if c.SessionIdleTimeout, err = positiveDuration("EAPAKA_WEBGUI_SESSION_IDLE_TIMEOUT", 30*time.Minute); err != nil {
		return Config{}, err
	}
	if c.SessionMaxAge, err = positiveDuration("EAPAKA_WEBGUI_SESSION_MAX_AGE", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if c.LoginMaxFailures, err = positiveInt("EAPAKA_WEBGUI_LOGIN_MAX_FAILURES", 5); err != nil {
		return Config{}, err
	}
	if c.LoginLockDuration, err = positiveDuration("EAPAKA_WEBGUI_LOGIN_LOCK_DURATION", 15*time.Minute); err != nil {
		return Config{}, err
	}
	if c.AuditMaxLen, err = positiveInt("EAPAKA_WEBGUI_AUDIT_MAX", 10000); err != nil {
		return Config{}, err
	}
	return c, nil
}

// CheckServe は、サーバーの起動に必須の設定がそろっているかを確かめる。
func (c Config) CheckServe() error {
	var errs []error
	if c.InitialAdminID == "" {
		errs = append(errs, errors.New("EAPAKA_WEBGUI_INITIAL_ADMIN_ID is not set"))
	}
	if c.InitialAdminPassword == "" {
		errs = append(errs, errors.New("EAPAKA_WEBGUI_INITIAL_ADMIN_PASSWORD is not set"))
	}
	if c.SessionIdleTimeout > c.SessionMaxAge {
		errs = append(errs, errors.New("EAPAKA_WEBGUI_SESSION_IDLE_TIMEOUT must not exceed EAPAKA_WEBGUI_SESSION_MAX_AGE"))
	}
	return errors.Join(errs...)
}

func positiveInt(name string, def int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: must be a positive integer", name)
	}
	return n, nil
}

// positiveDuration は 30m や 12h のような時間を読む。
func positiveDuration(name string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: must be a positive duration such as 30m or 12h", name)
	}
	return d, nil
}

func splitList(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
