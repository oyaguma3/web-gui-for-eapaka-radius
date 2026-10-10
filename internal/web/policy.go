package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// policiesPerPage は認可ポリシーの一覧の 1 ページの件数。
const policiesPerPage = 50

// maxPolicyRules は編集画面で扱うルールの数の上限。
const maxPolicyRules = 100

// ---- 一覧 ----

type policyListData struct {
	Prefix  string
	Items   []provapi.Policy
	Total   int64
	Cursor  string
	Next    string
	Message string
	Error   string
	// OpenIMSI と OpenError は「IMSI を指定して開く」の入力と誤り。
	OpenIMSI  string
	OpenError string
}

func (h *Handler) policies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := policyListData{Prefix: q.Get("prefix"), Cursor: q.Get("cursor")}
	if v := q.Get("deleted"); imsiPattern.MatchString(v) {
		d.Message = "IMSI " + v + " の認可ポリシーを削除しました。"
	}
	h.renderPolicies(w, r, http.StatusOK, d)
}

// renderPolicies は一覧を取得して画面を返す。
func (h *Handler) renderPolicies(w http.ResponseWriter, r *http.Request, status int, d policyListData) {
	if d.Prefix != "" && !prefixDigits.MatchString(d.Prefix) {
		d.Error = "IMSI の前方一致は 15 桁までの数字で指定してください。"
		h.render(w, r, http.StatusBadRequest, "policies", "認可ポリシー", d)
		return
	}
	list, err := h.prov.ListPolicies(r.Context(), provapi.ListParams{
		Prefix: d.Prefix, Cursor: d.Cursor, Limit: policiesPerPage,
	})
	if err != nil {
		var msg string
		status, msg = h.apiErrorMessage(err, "")
		h.log.Warn("list policies", "error", err)
		d.Error = msg
		h.render(w, r, status, "policies", "認可ポリシー", d)
		return
	}
	d.Items, d.Total, d.Next = list.Items, list.Total, list.NextCursor
	h.render(w, r, status, "policies", "認可ポリシー", d)
}

// policyOpen は、指定した IMSI の認可ポリシーの編集画面へ移動する（なければ新しく作る画面になる）。
func (h *Handler) policyOpen(w http.ResponseWriter, r *http.Request) {
	imsi := strings.TrimSpace(r.URL.Query().Get("imsi"))
	if !imsiPattern.MatchString(imsi) {
		h.renderPolicies(w, r, http.StatusBadRequest, policyListData{
			OpenIMSI: imsi, OpenError: "IMSI は 15 桁の数字で入力してください。",
		})
		return
	}
	http.Redirect(w, r, "/policies/"+imsi, http.StatusSeeOther)
}

// ---- 編集 ----

// ruleForm は編集中のルール。入力をそのまま持つ（保存するまで確かめない）。
type ruleForm struct {
	NASID string
	// SSIDs は許可する SSID を 1 行に 1 つずつ書いたもの。
	SSIDs          string
	VLANID         string
	SessionTimeout string
	// Errors はこのルールの項目ごとの誤り（キーは nasId / allowedSsids / vlanId / sessionTimeout）。
	Errors fieldErrors
	// Index は 0 からの番号、No は画面に出す 1 からの番号。First / Last は先頭・末尾か（上へ・下へのボタンに使う）。
	Index, No   int
	First, Last bool
}

type policyData struct {
	IMSI string
	// Exists は provisioning-api に保存済みか。
	Exists bool
	// Subscriber は同じ IMSI の加入者の有無（"yes" / "no"。確かめられなければ空）。
	Subscriber string
	Default    string
	Rules      []ruleForm
	// Status は保存済みの認可ポリシーの状態（active / suspended。0.3.0 以前の Provisioning API では空）。
	Status string
	// Dirty は、保存していない変更があるか。
	Dirty   bool
	Errors  fieldErrors
	Message string
	Error   string
	// ErrorOperation は provisioner の操作の記録の ID（同じ IMSI の未完了の操作で断られた場合など。リンクを出す）。
	ErrorOperation string
	// Provisioner は eapaka-node-provisioner 経由か（加入者の有無の説明を変える）。
	Provisioner bool
}

// formFromPolicy は保存済みのポリシーを編集の形にする。
func formFromPolicy(p provapi.Policy) (string, []ruleForm) {
	rules := make([]ruleForm, len(p.Rules))
	for i, rule := range p.Rules {
		rules[i] = ruleForm{
			NASID: rule.NASID, SSIDs: strings.Join(rule.AllowedSSIDs, "\n"), VLANID: rule.VLANID,
			Errors: fieldErrors{},
		}
		if rule.SessionTimeout != 0 {
			rules[i].SessionTimeout = strconv.Itoa(rule.SessionTimeout)
		}
	}
	return p.Default, rules
}

// policyIMSI は URL の IMSI を返す。形式が違えば 404 を返して false。
func (h *Handler) policyIMSI(w http.ResponseWriter, r *http.Request) (string, bool) {
	imsi := r.PathValue("imsi")
	if !imsiPattern.MatchString(imsi) {
		h.notFound(w, r)
		return "", false
	}
	return imsi, true
}

// subscriberState は、同じ IMSI の加入者の有無を返す（確かめられなければ空）。
// provisioner 経由のときは、鍵の置き場所（本PoC または aka-only-server）に鍵があるかで決める
// （provisioner の加入者は、認可ポリシーだけがある IMSI も返す）。
func (h *Handler) subscriberState(r *http.Request, imsi string) string {
	if h.pv != nil {
		sub, err := h.pv.GetSubscriber(r.Context(), imsi)
		switch {
		case err == nil && sub.Key != nil:
			return "yes"
		case err == nil || provapi.CauseOf(err) == provapi.CauseUserNotFound:
			return "no"
		}
		h.log.Warn("get subscriber", "error", err)
		return ""
	}
	_, err := h.prov.GetSubscriber(r.Context(), imsi)
	switch {
	case err == nil:
		return "yes"
	case provapi.CauseOf(err) == provapi.CauseUserNotFound:
		return "no"
	}
	h.log.Warn("get subscriber", "error", err)
	return ""
}

func (h *Handler) policy(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.policyIMSI(w, r)
	if !ok {
		return
	}
	d := policyData{IMSI: imsi, Default: provapi.PolicyDeny, Errors: fieldErrors{}}
	p, err := h.prov.GetPolicy(r.Context(), imsi)
	switch {
	case err == nil:
		d.Exists, d.Status = true, p.Status
		d.Default, d.Rules = formFromPolicy(p)
	case provapi.CauseOf(err) == provapi.CausePolicyNotFound:
		// まだない。既定は Admin TUI と同じく deny でルールなし。保存すると作成する。
	default:
		status, msg := h.apiErrorMessage(err, "")
		h.log.Warn("get policy", "error", err)
		h.renderErrorLink(w, r, status, msg, "/policies", "認可ポリシーの一覧へ")
		return
	}
	d.Subscriber = h.subscriberState(r, imsi)
	h.renderPolicy(w, r, http.StatusOK, d)
}

// numberRules はルールに番号と位置を書き込む。
func numberRules(rules []ruleForm) {
	for i := range rules {
		rules[i].Index, rules[i].No = i, i+1
		rules[i].First, rules[i].Last = i == 0, i == len(rules)-1
	}
}

// readPolicyForm は編集中のフォーム（ルールは同じ名前の項目の並び）を読む。
// state（保存済みの状態）と dirty（保存していない変更があるか）は、画面に出し直すために持ち回る値。
func readPolicyForm(r *http.Request) (policyData, error) {
	pf := r.PostForm
	d := policyData{
		Default: pf.Get("default"), Exists: pf.Get("exists") == "1", Subscriber: pf.Get("subscriber"),
		Status: pf.Get("state"), Dirty: pf.Get("dirty") == "1", Errors: fieldErrors{},
	}
	nas, ssids, vlans, timeouts := pf["nas_id"], pf["ssids"], pf["vlan_id"], pf["session_timeout"]
	if len(ssids) != len(nas) || len(vlans) != len(nas) || len(timeouts) != len(nas) || len(nas) > maxPolicyRules {
		return d, errors.New("rule fields mismatch")
	}
	for i := range nas {
		d.Rules = append(d.Rules, ruleForm{
			NASID: nas[i], SSIDs: ssids[i], VLANID: vlans[i], SessionTimeout: timeouts[i], Errors: fieldErrors{},
		})
	}
	return d, nil
}

// renderPolicy は編集の画面（htmx では編集の部分だけ）を返す。
func (h *Handler) renderPolicy(w http.ResponseWriter, r *http.Request, status int, d policyData) {
	numberRules(d.Rules)
	d.Provisioner = h.pv != nil
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "policy", "policy-detail", d)
		return
	}
	h.render(w, r, status, "policy", "認可ポリシー "+d.IMSI, d)
}

// policyEdit はルールの追加・削除・並べ替えを行った編集画面を返す。保存はしない（provisioning-api には送らない）。
// op は add / remove:N / up:N / down:N（N は 0 からのルールの番号）。空なら入力をそのまま返す（既定の動作の切り替え）。
func (h *Handler) policyEdit(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.policyIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	d, err := readPolicyForm(r)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。画面を開き直してください。")
		return
	}
	d.IMSI, d.Dirty = imsi, true
	op, arg, _ := strings.Cut(r.PostForm.Get("op"), ":")
	i, convErr := strconv.Atoi(arg)
	inRange := convErr == nil && i >= 0 && i < len(d.Rules)
	switch {
	case op == "add":
		if len(d.Rules) >= maxPolicyRules {
			d.Error = fmt.Sprintf("ルールは %d 件までです。", maxPolicyRules)
			break
		}
		// 新しいルールは、任意の NAS に一致する形で末尾に足す（上から順に評価するので、必要なら上へ動かす）。
		d.Rules = append(d.Rules, ruleForm{NASID: "*", Errors: fieldErrors{}})
	case op == "remove" && inRange:
		d.Rules = slices.Delete(d.Rules, i, i+1)
	case op == "up" && inRange && i > 0:
		d.Rules[i-1], d.Rules[i] = d.Rules[i], d.Rules[i-1]
	case op == "down" && inRange && i < len(d.Rules)-1:
		d.Rules[i], d.Rules[i+1] = d.Rules[i+1], d.Rules[i]
	}
	h.renderPolicy(w, r, http.StatusOK, d)
}

// splitSSIDs は 1 行に 1 つずつ書いた SSID を読む。前後の空白を除き、空行は無視する。
func splitSSIDs(v string) []string {
	var out []string
	for line := range strings.Lines(v) {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// toPolicyPut は編集中のフォームを確かめ、Provisioning API に送る形にする。誤りは d に書き込み、false を返す。
func toPolicyPut(d *policyData) (provapi.PolicyPut, bool) {
	ok := true
	if d.Default != provapi.PolicyAllow && d.Default != provapi.PolicyDeny {
		d.Errors["default"] = "既定の動作を選んでください。"
		ok = false
	}
	put := provapi.PolicyPut{Default: d.Default, Rules: []provapi.PolicyRule{}}
	for i := range d.Rules {
		f := &d.Rules[i]
		f.NASID, f.VLANID, f.SessionTimeout = strings.TrimSpace(f.NASID), strings.TrimSpace(f.VLANID), strings.TrimSpace(f.SessionTimeout)
		rule := provapi.PolicyRule{NASID: f.NASID, AllowedSSIDs: splitSSIDs(f.SSIDs), VLANID: f.VLANID}
		f.Errors.check(nasIDPattern.MatchString(f.NASID), "nasId",
			"NAS-ID は空白を含まない英数字・記号の 253 文字までで入力してください（* はすべての NAS）。")
		f.Errors.check(len(rule.AllowedSSIDs) > 0, "allowedSsids", "許可する SSID を 1 つ以上入力してください（* はすべての SSID）。")
		for j, s := range rule.AllowedSSIDs {
			f.Errors.check(len(s) <= maxSSIDLen, "allowedSsids",
				fmt.Sprintf("%d 行目の SSID が長すぎます（%d バイトまで）。", j+1, maxSSIDLen))
		}
		if f.VLANID != "" {
			v, err := strconv.Atoi(f.VLANID)
			f.Errors.check(vlanIDPattern.MatchString(f.VLANID) && err == nil && v <= maxVLANID, "vlanId",
				fmt.Sprintf("VLAN ID は 0〜%d の数字で入力してください（空なら割り当てない）。", maxVLANID))
		}
		if f.SessionTimeout != "" {
			v, err := strconv.Atoi(f.SessionTimeout)
			f.Errors.check(err == nil && v >= 0 && v <= maxSessionTimeout, "sessionTimeout",
				fmt.Sprintf("Session-Timeout は 0〜%d の秒数で入力してください（空か 0 なら送らない）。", maxSessionTimeout))
			rule.SessionTimeout = v
		}
		if len(f.Errors) > 0 {
			ok = false
		}
		put.Rules = append(put.Rules, rule)
	}
	return put, ok
}

// applyInvalidParams は Provisioning API の invalidParams を、該当するルールの項目の誤りとして書き込む。
// すべて対応付けられたら true を返す。
func applyInvalidParams(d *policyData, params []provapi.InvalidParam) bool {
	all := true
	for _, p := range params {
		if p.Param == "default" {
			d.Errors.check(false, "default", "既定の動作を選んでください。")
			continue
		}
		m := ruleParamPattern.FindStringSubmatch(p.Param)
		if m == nil {
			all = false
			continue
		}
		i, err := strconv.Atoi(m[1])
		if err != nil || i >= len(d.Rules) {
			all = false
			continue
		}
		msg := ruleFieldLabels[m[2]] + " が正しくありません。"
		if m[2] == "allowedSsids" && m[3] != "" {
			j, _ := strconv.Atoi(m[3])
			msg = fmt.Sprintf("%d 行目の SSID が正しくありません。", j+1)
		}
		d.Rules[i].Errors.check(false, m[2], msg)
	}
	return all
}

// policySave は編集中の内容で認可ポリシー全体を置き換える（なければ作る）。全員が使える。
func (h *Handler) policySave(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.policyIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	d, err := readPolicyForm(r)
	if err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。画面を開き直してください。")
		return
	}
	d.IMSI, d.Dirty = imsi, true
	put, valid := toPolicyPut(&d)
	if !valid {
		d.Error = "入力を確かめてください。"
		h.renderPolicy(w, r, http.StatusBadRequest, d)
		return
	}
	p, created, err := h.prov.PutPolicy(r.Context(), imsi, put)
	if err != nil {
		f := h.apiError(err, "")
		h.log.Warn("put policy", "error", err)
		if apiErr, ok := errors.AsType[*provapi.Error](err); ok && apiErr.Status == http.StatusBadRequest &&
			applyInvalidParams(&d, apiErr.Problem.InvalidParams) {
			f.Message = "入力を確かめてください。"
		}
		d.Error, d.ErrorOperation = f.Message, f.OperationID
		h.renderPolicy(w, r, f.Status, d)
		return
	}
	h.record(r, auditPolicyPut, imsi, map[string]any{"created": created, "default": p.Default, "rules": len(p.Rules)})
	saved := policyData{IMSI: imsi, Exists: true, Subscriber: d.Subscriber, Status: p.Status, Errors: fieldErrors{}}
	saved.Default, saved.Rules = formFromPolicy(p)
	saved.Message = "保存しました。すぐに認可に反映されます。"
	if created {
		saved.Message = "作成しました。すぐに認可に反映されます。"
	}
	h.renderPolicy(w, r, http.StatusOK, saved)
}

// policyDelete は認可ポリシーを削除する。全員が使える。
func (h *Handler) policyDelete(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.policyIMSI(w, r)
	if !ok {
		return
	}
	if err := h.prov.DeletePolicy(r.Context(), imsi); err != nil {
		h.log.Warn("delete policy", "error", err)
		h.renderFailure(w, r, h.apiError(err, notFoundMessage("IMSI "+imsi+" の認可ポリシー")), "/policies", "認可ポリシーの一覧へ")
		return
	}
	h.record(r, auditPolicyDelete, imsi, nil)
	seeOther(w, r, "/policies?deleted="+imsi)
}

// ---- 停止・再開 ----

// policyStatusLabel は加入者の状態（認可ポリシーの状態）の名前を返す。状態のない（0.3.0 以前の Provisioning API）
// 場合は空文字列。Valkey を直接書き換えた不正な値は、そのまま見せる（Auth Server はこの加入者の認証を拒否する）。
func policyStatusLabel(status string) string {
	switch status {
	case "":
		return ""
	case provapi.PolicyActive:
		return "利用中"
	case provapi.PolicySuspended:
		return "停止中"
	}
	return "不明な状態（" + status + "）"
}

// formPolicyStatus は停止・再開のフォームの status（変更後の状態）を返す。active / suspended でなければ false。
func formPolicyStatus(r *http.Request) (string, bool) {
	v := r.PostForm.Get("status")
	return v, v == provapi.PolicyActive || v == provapi.PolicySuspended
}

// policyStatusMessage は停止・再開が済んだときの文。
func policyStatusMessage(status string) string {
	if status == provapi.PolicySuspended {
		return "停止しました。次の認証から拒否します（接続中のセッションは切れません）。"
	}
	return "再開しました。次の認証から認可ポリシーのルールに従います。"
}

// changePolicyStatus は加入者を停止・再開し（認可ポリシーの状態を変える）、BFF の監査ログに残す。
// 認可ポリシーの画面と加入者の画面で共通。全員が使える（認可ポリシーの変更・削除と同じ）。
func (h *Handler) changePolicyStatus(r *http.Request, imsi, status string) (provapi.Policy, *apiFailure) {
	p, err := h.prov.SetPolicyStatus(r.Context(), imsi, status)
	if err != nil {
		f := h.apiError(err, notFoundMessage("IMSI "+imsi+" の認可ポリシー"))
		h.log.Warn("set policy status", "error", err)
		return provapi.Policy{}, &f
	}
	action := auditPolicyResume
	if status == provapi.PolicySuspended {
		action = auditPolicySuspend
	}
	h.record(r, action, imsi, nil)
	return p, nil
}

// policyStatus は認可ポリシーの画面から停止・再開を行う。編集中のフォームも一緒に受け取り、保存していない
// ルールの変更は保ったまま（保存はしない）状態だけを変えて返す。
func (h *Handler) policyStatus(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.policyIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	status, ok := formPolicyStatus(r)
	d, err := readPolicyForm(r)
	if !ok || err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。画面を開き直してください。")
		return
	}
	d.IMSI = imsi
	p, fail := h.changePolicyStatus(r, imsi, status)
	if fail != nil {
		d.Error, d.ErrorOperation = fail.Message, fail.OperationID
		h.renderPolicy(w, r, fail.Status, d)
		return
	}
	d.Exists, d.Status = true, p.Status
	if !d.Dirty {
		// 編集中の変更がなければ、最新の内容を出す（他の操作で変わっていても分かるように）。
		d.Default, d.Rules = formFromPolicy(p)
	}
	d.Message = policyStatusMessage(p.Status)
	h.renderPolicy(w, r, http.StatusOK, d)
}

// subscriberStatus は加入者の画面から停止・再開を行い、詳細の画面を返す。
func (h *Handler) subscriberStatus(w http.ResponseWriter, r *http.Request) {
	imsi, ok := h.subscriberIMSI(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	status, ok := formPolicyStatus(r)
	if !ok {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。画面を開き直してください。")
		return
	}
	p, fail := h.changePolicyStatus(r, imsi, status)
	if h.pv != nil {
		if fail != nil {
			h.renderPVSubscriberAfter(w, r, imsi, fail.Status, fail, func(d *pvSubscriberData) {
				d.Error, d.ErrorOperation = fail.Message, fail.OperationID
			})
			return
		}
		h.renderPVSubscriber(w, r, imsi, http.StatusOK, func(d *pvSubscriberData) { d.Message = policyStatusMessage(p.Status) })
		return
	}
	if fail != nil {
		h.renderSubscriber(w, r, imsi, fail.Status, func(d *subscriberData) { d.Error = fail.Message })
		return
	}
	h.renderSubscriber(w, r, imsi, http.StatusOK, func(d *subscriberData) { d.Message = policyStatusMessage(p.Status) })
}
