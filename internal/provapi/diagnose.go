package provapi

import (
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Diagnose は、Provisioning API に接続できなかったときの原因の見当を、利用者向けの文で返す。
// 見当がつかなければ空文字列を返す。
func Diagnose(err error) string {
	if err == nil {
		return ""
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return "provisioning-api のサーバー証明書に、接続先のホスト名（または IP アドレス）が入っていません。" +
			"本PoC側でサーバー証明書の SAN に接続先（同一ホストなら DNS:provisioning-api）を入れて作り直し" +
			"（B-02 §15.2）、BFF にも渡し直してください。"
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return "provisioning-api のサーバー証明書が、設定した証明書（EAPAKA_WEBGUI_ADMIN_SERVER_CERT）と一致しません。" +
			"本PoCの deployments/certs/provisioning/server.pem を設定してください。"
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return "provisioning-api のサーバー証明書が有効期間外です。本PoC側で作り直し（B-02 §15.2）、BFF にも渡し直してください。"
	}
	// 相手から TLS のアラートを受け取ると、Op が "remote error" の *net.OpError になる。
	// provisioning-api は未登録・有効期間外・証明書なしの接続を bad_certificate で拒否する。
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "remote error" &&
		(strings.Contains(opErr.Err.Error(), "bad certificate") || strings.Contains(opErr.Err.Error(), "certificate required")) {
		return "provisioning-api が BFF のクライアント証明書を受け付けませんでした。" +
			"本PoC側の .env の PROVISIONING_API_ADMIN_CLIENTS に、このクライアント証明書のフィンガープリントが登録されているか" +
			"（登録後に provisioning-api を作り直したか）、証明書が有効期間内かを確認してください。"
	}
	// TLS 1.3 では、provisioning-api がクライアント証明書を検証して拒否する前に、BFF はハンドシェイクを終えて
	// リクエストを送っている。provisioning-api が読まずに接続を閉じると、拒否のアラートより先に接続のリセットが届き、
	// アラートを受け取れないことがある（手元の契約テストで 16 回に 1 回ほど）。
	if errors.Is(err, syscall.ECONNRESET) {
		return "provisioning-api が接続を切りました。BFF のクライアント証明書が受け付けられなかった可能性があります。" +
			"本PoC側の .env の PROVISIONING_API_ADMIN_CLIENTS に、このクライアント証明書のフィンガープリントが登録されているか" +
			"（登録後に provisioning-api を作り直したか）、証明書が有効期間内かを確認してください" +
			"（provisioning-api のログに PROV_CLIENT_REJECTED が出ていれば、これが原因です）。"
	}
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok && dnsErr.IsNotFound {
		// 同一ホストの構成では、provisioning-api のコンテナが止まっているときも名前を解決できなくなる。
		return "provisioning-api のホスト名（" + dnsErr.Name + "）を解決できません。本PoCを " +
			"docker compose --profile provisioning up -d で起動しているか、同一ホストの場合は BFF が共有ネットワーク" +
			"（eapaka-prov）に参加しているか確認してください。"
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return "provisioning-api に接続できません。本PoCの provisioning-api が起動しているか（--profile provisioning）、" +
			"接続先の URL とポートが正しいか確認してください。"
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return "provisioning-api から時間内に応答がありません。接続先のアドレス、VPN、本PoC側の公開先（PROVISIONING_API_BIND）を確認してください。"
	}
	return ""
}
