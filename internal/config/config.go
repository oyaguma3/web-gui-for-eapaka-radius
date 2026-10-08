// Package config は環境変数から設定を読み込む。
package config

import (
	"cmp"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	}
	if v := os.Getenv("EAPAKA_WEBGUI_LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("EAPAKA_WEBGUI_LOG_LEVEL: %w", err)
		}
	}
	return c, nil
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
