package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// Provisioning API の入力形式（本PoCの pkg/validation と OpenAPI に合わせる）。
// Provisioning API に送る前に BFF でも確かめ、誤りを日本語で示す。
var (
	imsiPattern       = regexp.MustCompile(`^[0-9]{15}$`)
	prefixDigits      = regexp.MustCompile(`^[0-9]{1,15}$`)
	hex128            = regexp.MustCompile(`^[0-9a-f]{32}$`)
	sqnPattern        = regexp.MustCompile(`^[0-9a-f]{12}$`)
	amfPattern        = regexp.MustCompile(`^[0-9a-f]{4}$`)
	clientNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	vendorPattern     = regexp.MustCompile(`^[A-Za-z0-9 -]{0,64}$`)
	secretPattern     = regexp.MustCompile(`^[\x21-\x7E]{1,128}$`)
	nasIDPattern      = regexp.MustCompile(`^[\x21-\x7E]{1,253}$`)
	vlanIDPattern     = regexp.MustCompile(`^[0-9]{1,4}$`)
)

// 認可ポリシーの上限（本PoCの pkg/validation と同じ）。
const (
	maxSSIDLen        = 32
	maxVLANID         = 4094
	maxSessionTimeout = 86400
)

// validIPv4 は、IPv4 アドレスとして正しく、ドット区切りの 10 進で先頭に 0 のない表記かを返す。
func validIPv4(v string) bool {
	a, err := netip.ParseAddr(v)
	return err == nil && a.Is4() && a.String() == v
}

// fieldErrors は項目ごとの入力の誤り。
type fieldErrors map[string]string

func (f fieldErrors) check(ok bool, field, msg string) {
	if !ok {
		if _, dup := f[field]; !dup {
			f[field] = msg
		}
	}
}

// normalizeHex は 16 進の入力から前後の空白を除き、小文字にする。
func normalizeHex(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// fieldLabels は Provisioning API の項目名を画面の名前にする。
var fieldLabels = map[string]string{
	"imsi":    "IMSI",
	"ki":      "Ki",
	"opc":     "OPc",
	"sqn":     "SQN",
	"amf":     "AMF",
	"ip":      "IP アドレス",
	"secret":  "共有シークレット",
	"name":    "名前",
	"vendor":  "ベンダー",
	"default": "既定の動作",
	"rules":   "ルール",
}

// ruleParamPattern は認可ポリシーのルールの項目名（例: rules[0].allowedSsids[1]）。
var ruleParamPattern = regexp.MustCompile(`^rules\[(\d+)\]\.(nasId|allowedSsids|vlanId|sessionTimeout)(?:\[(\d+)\])?$`)

// ruleFieldLabels はルールの項目名を画面の名前にする。
var ruleFieldLabels = map[string]string{
	"nasId":          "NAS-ID",
	"allowedSsids":   "許可する SSID",
	"vlanId":         "VLAN ID",
	"sessionTimeout": "Session-Timeout",
}

// paramLabel は invalidParams の項目名を画面の名前にする。
func paramLabel(p string) string {
	if m := ruleParamPattern.FindStringSubmatch(p); m != nil {
		i, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("ルール %d の %s", i+1, ruleFieldLabels[m[2]])
	}
	return cmp.Or(fieldLabels[p], p)
}

// apiErrorMessage は Provisioning API の呼び出しのエラーを、ステータスと利用者向けの説明にする。
// notFound は対象が見つからないときの説明（空なら cause に応じた既定の文）。
// 本PoCの Admin TUI と同時に使った場合の 404 / 409 は、他の操作によるものと分かる文にする。
func apiErrorMessage(err error, notFound string) (int, string) {
	if provapi.IsUnavailable(err) {
		return http.StatusBadGateway, provErrorMessage(err)
	}
	apiErr, ok := errors.AsType[*provapi.Error](err)
	if !ok {
		return http.StatusInternalServerError, "処理に失敗しました。"
	}
	const (
		elsewhere           = "他の操作（本PoCの Admin TUI など）で変更された可能性があります。"
		registeredElsewhere = "（本PoCの Admin TUI など、他の操作で登録された場合もあります）。"
	)
	switch apiErr.Problem.Cause {
	case provapi.CauseUserNotFound:
		return http.StatusNotFound, cmp.Or(notFound, "加入者が見つかりません。"+elsewhere)
	case provapi.CauseClientNotFound:
		return http.StatusNotFound, cmp.Or(notFound, "RADIUSクライアントが見つかりません。"+elsewhere)
	case provapi.CausePolicyNotFound:
		return http.StatusNotFound, cmp.Or(notFound, "認可ポリシーが見つかりません。"+elsewhere)
	case provapi.CauseSubscriberExists:
		return http.StatusConflict, "その IMSI の加入者は既に登録されています" + registeredElsewhere
	case provapi.CauseClientExists:
		return http.StatusConflict, "その IP アドレスの RADIUSクライアントは既に登録されています" + registeredElsewhere
	}
	if apiErr.Status == http.StatusBadRequest && len(apiErr.Problem.InvalidParams) > 0 {
		var names []string
		for _, p := range apiErr.Problem.InvalidParams {
			names = append(names, paramLabel(p.Param))
		}
		return http.StatusBadRequest, "入力が正しくありません（" + strings.Join(names, "、") + "）。"
	}
	if apiErr.Status == http.StatusNotFound {
		return http.StatusNotFound, cmp.Or(notFound, "対象が見つかりません。")
	}
	return http.StatusBadGateway, "本PoCの Provisioning API がエラーを返しました。"
}

// notFoundMessage は、対象が見つからないときの説明を作る。
func notFoundMessage(what string) string {
	return what + " は登録されていません。他の操作（本PoCの Admin TUI など）で削除された可能性があります。"
}
