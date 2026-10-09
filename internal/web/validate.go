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
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// Provisioning API の入力形式（本PoCの pkg/validation と OpenAPI に合わせる）。
// Provisioning API に送る前に BFF でも確かめ、誤りを日本語で示す。
var (
	imsiPattern  = regexp.MustCompile(`^[0-9]{15}$`)
	prefixDigits = regexp.MustCompile(`^[0-9]{1,15}$`)
	// streamIDPattern は provisioning-api の監査ログのエントリID（Valkey の Stream の ID）の形式。
	streamIDPattern   = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,20}$`)
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

// apiFailure は、接続先（provisioning-api または provisioner）の呼び出しの失敗を画面に出す形にしたもの。
type apiFailure struct {
	Status  int
	Message string
	// OperationID は、provisioner の操作の記録の ID（途中で失敗した操作、または同じ IMSI の未完了の操作）。
	// 画面は「操作の記録」へのリンクを出す。
	OperationID string
}

// apiErrorMessage は接続先の呼び出しのエラーを、ステータスと利用者向けの説明にする。
// notFound は対象が見つからないときの説明（空なら cause に応じた既定の文）。
func (h *Handler) apiErrorMessage(err error, notFound string) (int, string) {
	f := h.apiError(err, notFound)
	return f.Status, f.Message
}

// apiError は接続先の呼び出しのエラーを、画面に出す形にする。
// 本PoCの Admin TUI と同時に使った場合の 404 / 409 は、他の操作によるものと分かる文にする。
func (h *Handler) apiError(err error, notFound string) apiFailure {
	if provapi.IsUnavailable(err) {
		return apiFailure{Status: http.StatusBadGateway, Message: h.provErrorMessage(err)}
	}
	apiErr, ok := errors.AsType[*provapi.Error](err)
	if !ok {
		return apiFailure{Status: http.StatusInternalServerError, Message: "処理に失敗しました。"}
	}
	const (
		elsewhere           = "他の操作（本PoCの Admin TUI など）で変更された可能性があります。"
		registeredElsewhere = "（本PoCの Admin TUI など、他の操作で登録された場合もあります）。"
	)
	p := apiErr.Problem
	switch p.Cause {
	case provapi.CauseUserNotFound:
		return apiFailure{Status: http.StatusNotFound, Message: cmp.Or(notFound, "加入者が見つかりません。"+elsewhere)}
	case provapi.CauseClientNotFound:
		return apiFailure{Status: http.StatusNotFound, Message: cmp.Or(notFound, "RADIUSクライアントが見つかりません。"+elsewhere)}
	case provapi.CausePolicyNotFound:
		return apiFailure{Status: http.StatusNotFound, Message: cmp.Or(notFound, "認可ポリシーが見つかりません。"+elsewhere)}
	case provapi.CauseSubscriberExists:
		msg := "その IMSI の加入者は既に登録されています" + registeredElsewhere
		if places := conflictPlaces(p.Conflicts); places != "" {
			msg = "その IMSI は既に登録されています（" + places + "）" + registeredElsewhere
		}
		return apiFailure{Status: http.StatusConflict, Message: msg, OperationID: p.OperationID}
	case provapi.CauseClientExists:
		return apiFailure{Status: http.StatusConflict, Message: "その IP アドレスの RADIUSクライアントは既に登録されています" + registeredElsewhere}
	}
	if f, ok := provisionerFailure(apiErr); ok {
		return f
	}
	if apiErr.Status == http.StatusBadRequest && len(p.InvalidParams) > 0 {
		var names []string
		for _, ip := range p.InvalidParams {
			names = append(names, paramLabel(ip.Param))
		}
		return apiFailure{Status: http.StatusBadRequest, Message: "入力が正しくありません（" + strings.Join(names, "、") + "）。"}
	}
	if apiErr.Status == http.StatusNotFound {
		return apiFailure{Status: http.StatusNotFound, Message: cmp.Or(notFound, "対象が見つかりません。")}
	}
	return apiFailure{Status: http.StatusBadGateway, Message: h.apiName() + " がエラーを返しました。"}
}

// downstreamLabels は provisioner の下流の名前。
var downstreamLabels = map[string]string{"prov": "本PoCの Provisioning API", "aka": "aka-only-server"}

// spaceBeforeASCII は、英数字で始まる語の前に空白を入れる（「provisioner から aka-only-server」「provisioner から本PoC」）。
func spaceBeforeASCII(s string) string {
	if s != "" && s[0] < 0x80 {
		return " " + s
	}
	return s
}

// downstreamLabel は provisioner の下流（prov / aka）の画面の名前を返す。
func downstreamLabel(name string) string { return cmp.Or(downstreamLabels[name], name) }

// conflictPlaces は、provisioner が SUBSCRIBER_ALREADY_EXISTS で返した場所（poc / aka / policy）を画面の言葉にする。
func conflictPlaces(places []string) string {
	labels := map[string]string{"poc": "本PoCの鍵", "aka": "aka-only-server の鍵", "policy": "認可ポリシー"}
	var out []string
	for _, p := range places {
		out = append(out, cmp.Or(labels[p], p))
	}
	return strings.Join(out, "、")
}

// provisionerFailure は provisioner だけが返すエラー（pvapi の cause）を画面に出す形にする。該当しなければ false。
func provisionerFailure(apiErr *provapi.Error) (apiFailure, bool) {
	p := apiErr.Problem
	// rolledBack は、書き込みの途中で失敗し、補償で元に戻したことを示す（このときは operationId も付く）。
	var rolledBack string
	if p.RolledBack != nil && *p.RolledBack {
		rolledBack = "途中まで行った変更は元に戻しました。"
	}
	switch p.Cause {
	case pvapi.CauseOperationInProgress:
		return apiFailure{Status: http.StatusConflict,
			Message: "同じ IMSI の操作が処理中です。少し待ってから、もう一度操作してください。"}, true
	case pvapi.CauseOperationUnresolved:
		return apiFailure{Status: http.StatusConflict, OperationID: p.OperationID,
			Message: "同じ IMSI に完了していない操作が残っています。「操作の記録」で、その操作をやり直すか閉じてから、もう一度操作してください。"}, true
	case pvapi.CauseOperationIncomplete:
		return apiFailure{Status: http.StatusInternalServerError, OperationID: p.OperationID,
			Message: downstreamLabel(p.Downstream) + " への操作が途中で失敗し、元に戻せませんでした。provisioner が後で自動でやり直します。「操作の記録」で状況を確かめてください。"}, true
	case pvapi.CauseDownstreamUnavailable:
		return apiFailure{Status: http.StatusServiceUnavailable, OperationID: p.OperationID,
			Message: "provisioner から" + spaceBeforeASCII(downstreamLabel(p.Downstream)) + " に接続できません。" + rolledBack + "ダッシュボードで接続の状態を確かめてください。"}, true
	case pvapi.CauseDownstreamError:
		return apiFailure{Status: http.StatusBadGateway, OperationID: p.OperationID,
			Message: downstreamLabel(p.Downstream) + " がエラーを返しました（" + cmp.Or(p.DownstreamCause, strconv.Itoa(p.DownstreamStatus)) + "）。" + rolledBack}, true
	case pvapi.CauseOperationStateConflict:
		return apiFailure{Status: http.StatusConflict,
			Message: "この操作の記録の状態では、その操作はできません（やり直せるのは「失敗」の操作だけ、閉じられるのは「やり直し中」と「失敗」の操作だけです。" +
				"鍵の変更が反映されたか分からない変更はやり直せないので、下流の状態を確かめて直した後に閉じてください）。"}, true
	case pvapi.CauseKeyStoreMismatch:
		return apiFailure{Status: http.StatusBadRequest,
			Message: "鍵の置き場所が、provisioner の PLMN マップから決まるものと違います。"}, true
	case pvapi.CauseIdempotencyKeyMismatch:
		return apiFailure{Status: http.StatusConflict,
			Message: "送信の内容が前回と違います。画面を開き直してから、もう一度操作してください。"}, true
	case pvapi.CauseDownstreamNotConfigured:
		return apiFailure{Status: http.StatusNotFound,
			Message: "provisioner は aka-only-server を扱わない設定です。"}, true
	}
	return apiFailure{}, false
}

// apiName は接続先の名前（画面の文に使う）。
func (h *Handler) apiName() string {
	if h.pv != nil {
		return "provisioner"
	}
	return "本PoCの Provisioning API"
}

// provErrorMessage は、接続先の呼び出しに失敗したときに画面に出す説明を返す。
func (h *Handler) provErrorMessage(err error) string {
	if provapi.IsUnavailable(err) {
		hint := provapi.Diagnose(err)
		if h.pv != nil {
			hint = pvapi.Diagnose(err)
		}
		return cmp.Or(hint, h.apiName()+" に接続できません。")
	}
	return h.apiName() + " がエラーを返しました。"
}

// monitoringErrorMessage は、監査ログ・セッションの参照（Provisioning API 0.3.0 から）に失敗したときの
// ステータスコードと説明を返す。what は「監査ログの参照」などの機能の名前。
// 0.3.0 より前の provisioning-api は、存在しないパスとして 404（cause なし）を返す。
func (h *Handler) monitoringErrorMessage(err error, what string) (int, string) {
	if apiErr, ok := errors.AsType[*provapi.Error](err); ok && apiErr.Status == http.StatusNotFound && apiErr.Problem.Cause == "" {
		return http.StatusBadGateway, "本PoCの Provisioning API が" + what + "に対応していません（provisioning-api 0.3.0 以降が必要です）。"
	}
	return h.apiErrorMessage(err, "")
}

// notFoundMessage は、対象が見つからないときの説明を作る。
func notFoundMessage(what string) string {
	return what + " は登録されていません。他の操作（本PoCの Admin TUI など）で削除された可能性があります。"
}
