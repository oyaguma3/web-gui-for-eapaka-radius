package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// policyForm は編集画面のフォームの値を作る。rules は {NAS-ID, SSID（改行区切り）, VLAN ID, Session-Timeout} の並び。
func policyForm(def string, rules ...[4]string) url.Values {
	v := url.Values{"default": {def}, "subscriber": {"no"}}
	for _, r := range rules {
		v.Add("nas_id", r[0])
		v.Add("ssids", r[1])
		v.Add("vlan_id", r[2])
		v.Add("session_timeout", r[3])
	}
	return v
}

func TestPolicyListAndOpen(t *testing.T) {
	e := newScreenEnv(t)
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny",
		Rules: []provapi.PolicyRule{{NASID: "*", AllowedSSIDs: []string{"A"}}, {NASID: "AP", AllowedSSIDs: []string{"B"}}}}

	body := do(e.h, request("GET", "/policies", e.user, nil)).Body.String()
	if !strings.Contains(body, "全 1 件") || !strings.Contains(body, `<a href="/policies/001010000000001">`) || !strings.Contains(body, "<td>2 件</td>") {
		t.Errorf("list: %s", body)
	}
	w := do(e.h, request("GET", "/policies/open?imsi=001010000000002", e.user, nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/policies/001010000000002" {
		t.Errorf("open: %d %v", w.Code, w.Header())
	}
	w = do(e.h, request("GET", "/policies/open?imsi=0010", e.user, nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "IMSI は 15 桁の数字で入力してください。") || !strings.Contains(w.Body.String(), `value="0010"`) {
		t.Errorf("open invalid: %d", w.Code)
	}
}

func TestPolicyNewAndEditOps(t *testing.T) {
	e := newScreenEnv(t)

	// まだないポリシーは、deny・ルールなしで新しく作る画面になる。加入者の有無も出す。
	body := do(e.h, request("GET", "/policies/001010000000001", e.user, nil)).Body.String()
	if !strings.Contains(body, "まだありません") || !strings.Contains(body, `value="deny" checked`) ||
		!strings.Contains(body, "ルールはありません") || !strings.Contains(body, "本PoCには登録されていません") ||
		strings.Contains(body, "この認可ポリシーを削除") || strings.Contains(body, "hx-confirm=\"既定の動作が allow") {
		t.Errorf("new: %s", body)
	}

	calls := len(e.prov.calls)
	edit := func(op string, form url.Values) string {
		t.Helper()
		form.Set("op", op)
		w := do(e.h, hx(request("POST", "/policies/001010000000001/edit", e.user, form)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", op, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	// 追加すると、任意の NAS に一致するルールを末尾に足す。
	body = edit("add", policyForm("deny"))
	if !strings.Contains(body, "ルール 1") || !strings.Contains(body, `name="nas_id" value="*"`) || !strings.Contains(body, "保存していない変更があります") {
		t.Errorf("add: %s", body)
	}
	two := policyForm("deny", [4]string{"AP-01", "CORP\nLAB", "100", "3600"}, [4]string{"*", "GUEST", "", ""})
	// 下へ・上へで入れ替え、入力はそのまま運ぶ。
	body = edit("down:0", two)
	if i, j := strings.Index(body, `value="*"`), strings.Index(body, `value="AP-01"`); i < 0 || j < 0 || i > j ||
		!strings.Contains(body, "CORP\nLAB</textarea>") {
		t.Errorf("down: %s", body)
	}
	body = edit("up:1", two)
	if i, j := strings.Index(body, `value="*"`), strings.Index(body, `value="AP-01"`); i < 0 || j < 0 || i > j {
		t.Errorf("up: %s", body)
	}
	// 先頭は上へ、末尾は下へ動かせない。
	if !strings.Contains(body, `value="up:0" hx-post="/policies/001010000000001/edit" disabled`) ||
		!strings.Contains(body, `value="down:1" hx-post="/policies/001010000000001/edit" disabled`) {
		t.Errorf("disabled buttons: %s", body)
	}
	body = edit("remove:0", two)
	if strings.Contains(body, `value="AP-01"`) || !strings.Contains(body, `value="*"`) || strings.Contains(body, "ルール 2") {
		t.Errorf("remove: %s", body)
	}
	// 既定の動作を allow にすると、保存に確認を付ける。
	body = edit("", policyForm("allow"))
	if !strings.Contains(body, `hx-confirm="既定の動作が allow です。`) || !strings.Contains(body, `hx-disinherit="hx-confirm"`) {
		t.Errorf("allow: %s", body)
	}
	// 編集の操作では Provisioning API を呼ばない。
	if len(e.prov.calls) != calls {
		t.Errorf("calls during edit: %v", e.prov.calls[calls:])
	}

	// 項目の数がそろわないフォームは読まない。
	broken := policyForm("deny", [4]string{"*", "A", "", ""})
	broken.Add("nas_id", "extra")
	if w := do(e.h, hx(request("POST", "/policies/001010000000001/edit", e.user, broken))); w.Code != http.StatusBadRequest {
		t.Errorf("broken: %d", w.Code)
	}
}

func TestPolicySave(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")
	path := "/policies/001010000000001"

	// 保存前に BFF で確かめ、誤りは該当するルールの項目に出す。
	bad := policyForm("deny",
		[4]string{"", "A", "", ""},
		[4]string{"*", "OK\n" + strings.Repeat("x", 33), "5000", "x"},
		[4]string{"*", " \n", "", ""})
	w := do(e.h, hx(request("POST", path, e.user, bad)))
	body := w.Body.String()
	for _, want := range []string{"NAS-ID は空白を含まない", "2 行目の SSID が長すぎます", "VLAN ID は 0〜4094", "Session-Timeout は 0〜86400", "許可する SSID を 1 つ以上"} {
		if !strings.Contains(body, want) {
			t.Errorf("invalid: missing %q", want)
		}
	}
	if w.Code != http.StatusBadRequest || slices.Contains(e.prov.calls, "PutPolicy bob") {
		t.Errorf("invalid: %d %v", w.Code, e.prov.calls)
	}

	// 作成。SSID は行ごとに分け、前後の空白と空行を除く。
	good := policyForm("deny", [4]string{" AP-01 ", " CORP \n\nLAB\n", "100", "3600"}, [4]string{"*", "*", "", ""})
	w = do(e.h, hx(request("POST", path, e.user, good)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "作成しました。") || strings.Contains(w.Body.String(), "まだありません") {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	want := provapi.PolicyPut{Default: "deny", Rules: []provapi.PolicyRule{
		{NASID: "AP-01", AllowedSSIDs: []string{"CORP", "LAB"}, VLANID: "100", SessionTimeout: 3600},
		{NASID: "*", AllowedSSIDs: []string{"*"}},
	}}
	if got := e.prov.lastPolicyPut; got.Default != want.Default || !slices.EqualFunc(got.Rules, want.Rules, func(a, b provapi.PolicyRule) bool {
		return a.NASID == b.NASID && slices.Equal(a.AllowedSSIDs, b.AllowedSSIDs) && a.VLANID == b.VLANID && a.SessionTimeout == b.SessionTimeout
	}) || e.prov.lastCall() != "PutPolicy bob" {
		t.Errorf("put = %+v", got)
	}
	if a := e.lastAudit(); a.Action != auditPolicyPut || a.Target != "001010000000001" || !strings.Contains(a.Detail, `"created":true`) ||
		!strings.Contains(a.Detail, `"rules":2`) {
		t.Errorf("audit = %+v", a)
	}

	// 置き換え（ルールなしにもできる）。
	w = do(e.h, hx(request("POST", path, e.user, policyForm("allow"))))
	if !strings.Contains(w.Body.String(), "保存しました。") || e.prov.lastPolicyPut.Rules == nil || len(e.prov.lastPolicyPut.Rules) != 0 {
		t.Errorf("replace: %s %+v", w.Body.String(), e.prov.lastPolicyPut)
	}
	body = do(e.h, request("GET", path, e.user, nil)).Body.String()
	if !strings.Contains(body, `value="allow" checked`) || !strings.Contains(body, `<a href="/subscribers/001010000000001">登録あり</a>`) ||
		!strings.Contains(body, "この認可ポリシーを削除") {
		t.Errorf("reload: %s", body)
	}

	// Provisioning API の invalidParams は、ルールと SSID の行に対応付ける。
	e.prov.putPolicyErr = &provapi.Error{Status: 400, Problem: provapi.Problem{Status: 400, Cause: provapi.CauseMandatoryIEIncorrect,
		InvalidParams: []provapi.InvalidParam{{Param: "rules[0].allowedSsids[1]", Reason: "bad"}, {Param: "rules[0].vlanId"}}}}
	w = do(e.h, hx(request("POST", path, e.user, good)))
	body = w.Body.String()
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "2 行目の SSID が正しくありません。") ||
		!strings.Contains(body, "VLAN ID が正しくありません。") || !strings.Contains(body, "入力を確かめてください。") {
		t.Errorf("invalidParams: %d %s", w.Code, body)
	}
}

func TestPolicyDelete(t *testing.T) {
	e := newScreenEnv(t)
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny"}
	w := do(e.h, hx(request("POST", "/policies/001010000000001/delete", e.user, url.Values{})))
	if w.Header().Get("HX-Redirect") != "/policies?deleted=001010000000001" || e.lastAudit().Action != auditPolicyDelete {
		t.Errorf("delete: %d %v", w.Code, w.Header())
	}
	if body := do(e.h, request("GET", "/policies?deleted=001010000000001", e.user, nil)).Body.String(); !strings.Contains(body, "の認可ポリシーを削除しました。") {
		t.Errorf("deleted: %s", body)
	}
	w = do(e.h, hx(request("POST", "/policies/001010000000001/delete", e.user, url.Values{})))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "他の操作") || !strings.Contains(w.Body.String(), `href="/policies"`) {
		t.Errorf("already deleted: %d %s", w.Code, w.Body.String())
	}
}
