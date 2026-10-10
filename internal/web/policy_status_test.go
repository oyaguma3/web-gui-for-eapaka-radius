package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// 加入者の停止・再開（認可ポリシーの状態。設計概要 §13、画面仕様 §8.3）。

func TestPolicyStatusOnPolicyScreen(t *testing.T) {
	e := newScreenEnv(t)
	const imsi = "001010000000001"
	e.prov.policies[imsi] = provapi.Policy{IMSI: imsi, Default: "deny", Status: provapi.PolicyActive,
		Rules: []provapi.PolicyRule{{NASID: "AP-01", AllowedSSIDs: []string{"CORP"}}}}
	e.prov.policies["001010000000002"] = provapi.Policy{IMSI: "001010000000002", Default: "allow", Status: provapi.PolicySuspended}

	// 一覧に状態を出す（停止中は目立たせる）。
	body := do(e.h, request("GET", "/policies", e.user, nil)).Body.String()
	mustContain(t, body, `<th scope="col">状態</th>`, "<td>利用中</td>", `<strong class="status-suspended">停止中</strong>`)

	// 編集画面: 利用中なら「停止する」だけを出す。編集中のフォームも一緒に送る。
	body = do(e.h, request("GET", "/policies/"+imsi, e.user, nil)).Body.String()
	mustContain(t, body, "<strong>利用中</strong>", `hx-post="/policies/001010000000001/status"`, `hx-vals='{"status":"suspended"}'`,
		`hx-include="#policy-form"`, `id="policy-form"`, `name="state" value="active"`, "加入者 001010000000001 を停止します。")
	if strings.Contains(body, "再開する") {
		t.Error("active policy shows resume")
	}

	// 一般ユーザーも停止できる。編集中の変更がなければ、最新の内容を出す。
	form := policyForm("deny", [4]string{"AP-01", "CORP", "", ""})
	form.Set("exists", "1")
	form.Set("state", "active")
	form.Set("status", "suspended")
	w := do(e.h, hx(request("POST", "/policies/"+imsi+"/status", e.user, form)))
	if w.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", w.Code, w.Body)
	}
	body = w.Body.String()
	mustContain(t, body, "停止しました。次の認証から拒否します", `<strong class="status-suspended">停止中</strong>`, "再開する",
		`name="state" value="suspended"`)
	if strings.Contains(body, "停止する</button>") || strings.Contains(body, "保存していない変更があります") {
		t.Errorf("after suspend: %s", body)
	}
	if got := e.prov.lastCall(); got != "SetPolicyStatus:suspended bob" {
		t.Errorf("call = %q", got)
	}
	if a := e.lastAudit(); a.Action != auditPolicySuspend || a.Target != imsi {
		t.Errorf("audit = %+v", a)
	}

	// 保存していないルールの変更は、そのまま残す（保存はしない）。
	form = policyForm("deny", [4]string{"AP-99", "LAB", "", ""})
	form.Set("exists", "1")
	form.Set("state", "suspended")
	form.Set("dirty", "1")
	form.Set("status", "active")
	body = do(e.h, hx(request("POST", "/policies/"+imsi+"/status", e.user, form))).Body.String()
	mustContain(t, body, "再開しました。", "<strong>利用中</strong>", `value="AP-99"`, "保存していない変更があります",
		`name="dirty" value="1"`, "保存していないルールの変更はそのまま残ります")
	if p := e.prov.policies[imsi]; p.Status != provapi.PolicyActive || p.Rules[0].NASID != "AP-01" {
		t.Errorf("policy = %+v", p)
	}
	if a := e.lastAudit(); a.Action != auditPolicyResume {
		t.Errorf("audit = %+v", a)
	}

	// 保存（全体の置き換え）では状態を変えない。
	e.prov.policies[imsi] = provapi.Policy{IMSI: imsi, Default: "deny", Status: provapi.PolicySuspended}
	form = policyForm("deny")
	form.Set("exists", "1")
	form.Set("state", "suspended")
	body = do(e.h, hx(request("POST", "/policies/"+imsi, e.user, form))).Body.String()
	mustContain(t, body, "保存しました。", `<strong class="status-suspended">停止中</strong>`)

	// 不正な値は送らない。
	calls := len(e.prov.calls)
	form.Set("status", "paused")
	if w := do(e.h, hx(request("POST", "/policies/"+imsi+"/status", e.user, form))); w.Code != http.StatusBadRequest || len(e.prov.calls) != calls {
		t.Errorf("bad status = %d", w.Code)
	}

	// 他の操作で認可ポリシーが削除されていた。
	form.Set("status", "suspended")
	w = do(e.h, hx(request("POST", "/policies/001010000000009/status", e.user, form)))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "IMSI 001010000000009 の認可ポリシー は登録されていません") {
		t.Errorf("not found = %d %s", w.Code, w.Body)
	}
}

func TestPolicyStatusUnsupportedAndUnknown(t *testing.T) {
	e := newScreenEnv(t)
	// 0.3.0 以前の Provisioning API は状態を返さない。ボタンを出さない。
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny"}
	body := do(e.h, request("GET", "/policies/001010000000001", e.user, nil)).Body.String()
	mustContain(t, body, "接続先が停止・再開に対応していません（本PoCの provisioning-api 0.4.0 以降で使えます）。")
	if strings.Contains(body, "/status\"") {
		t.Error("unsupported shows buttons")
	}
	if body := do(e.h, request("GET", "/policies", e.user, nil)).Body.String(); !strings.Contains(body, "<td>-</td>") {
		t.Errorf("list: %s", body)
	}
	// まだない認可ポリシーには状態を出さない。
	if body := do(e.h, request("GET", "/policies/001010000000002", e.user, nil)).Body.String(); strings.Contains(body, "状態:") {
		t.Error("new policy shows status")
	}

	// Valkey を直接書き換えた不正な値は、そのまま見せて、どちらにも戻せるようにする。
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny", Status: "paused"}
	body = do(e.h, request("GET", "/policies/001010000000001", e.user, nil)).Body.String()
	mustContain(t, body, "不明な状態（paused）", "停止または再開で正しい状態に戻せます", "停止する", "再開する")
}

func TestSubscriberStatus(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")
	e.seed(t, "001010000000002")
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny", Status: provapi.PolicySuspended}

	// 認可ポリシーがあれば状態と停止・再開を出す。
	body := do(e.h, request("GET", "/subscribers/001010000000001", e.user, nil)).Body.String()
	mustContain(t, body, `<th scope="row">状態</th>`, `<strong class="status-suspended">停止中</strong>`,
		`hx-post="/subscribers/001010000000001/status"`, `hx-target="#subscriber"`, "再開する")
	if strings.Contains(body, "hx-include") {
		t.Error("subscriber screen includes a form")
	}
	// 認可ポリシーがなければ出さない（停止できない）。
	if body := do(e.h, request("GET", "/subscribers/001010000000002", e.user, nil)).Body.String(); strings.Contains(body, `<th scope="row">状態</th>`) {
		t.Error("no policy shows status")
	}

	w := do(e.h, hx(request("POST", "/subscribers/001010000000001/status", e.user, url.Values{"status": {"active"}})))
	if w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body)
	}
	mustContain(t, w.Body.String(), "再開しました。", "<strong>利用中</strong>", "停止する")
	if got := e.prov.lastCall(); got != "SetPolicyStatus:active bob" {
		t.Errorf("call = %q", got)
	}
	if a := e.lastAudit(); a.Action != auditPolicyResume || a.Target != "001010000000001" {
		t.Errorf("audit = %+v", a)
	}

	// 失敗したら詳細の画面に文を出す。
	e.prov.setStatusErr = &provapi.Error{Status: 502, Problem: provapi.Problem{Status: 500, Cause: "SYSTEM_FAILURE"}}
	w = do(e.h, hx(request("POST", "/subscribers/001010000000001/status", e.user, url.Values{"status": {"suspended"}})))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "エラーを返しました") {
		t.Errorf("failure = %d %s", w.Code, w.Body)
	}
	if w := do(e.h, hx(request("POST", "/subscribers/001010000000001/status", e.user, url.Values{"status": {""}}))); w.Code != http.StatusBadRequest {
		t.Errorf("empty status = %d", w.Code)
	}
}

func TestPVSubscriberStatus(t *testing.T) {
	e := newPVEnv(t)
	e.seedPV("001020000000001")
	e.seedPV("001010000000002")
	sub := e.pv.subs["001020000000001"]
	sub.Status = provapi.PolicySuspended
	e.pv.subs["001020000000001"] = sub
	active := e.pv.subs["001010000000002"]
	active.Status = provapi.PolicyActive
	e.pv.subs["001010000000002"] = active
	e.prov.policies["001020000000001"] = provapi.Policy{IMSI: "001020000000001", Default: "deny", Status: provapi.PolicySuspended}

	// 一覧に状態の列を出す。
	body := do(e.h, request("GET", "/subscribers", e.user, nil)).Body.String()
	mustContain(t, body, `<th scope="col">状態</th>`, `<strong class="status-suspended">停止中</strong>`, "<td>利用中</td>")

	// 詳細: 状態と再開のボタン。
	body = do(e.h, request("GET", "/subscribers/001020000000001", e.user, nil)).Body.String()
	mustContain(t, body, `<strong class="status-suspended">停止中</strong>`, `hx-post="/subscribers/001020000000001/status"`, "再開する")

	// 停止・再開は、認可ポリシーの中継（Provisioning API と同じ形）で送る。
	w := do(e.h, hx(request("POST", "/subscribers/001020000000001/status", e.user, url.Values{"status": {"active"}})))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "再開しました。") {
		t.Fatalf("resume = %d %s", w.Code, w.Body)
	}
	if got := e.prov.lastCall(); got != "SetPolicyStatus:active bob" {
		t.Errorf("call = %q", got)
	}

	// 未完了の操作で断られたら、操作の記録へのリンクを出す。
	e.prov.setStatusErr = &provapi.Error{Status: 409, Problem: provapi.Problem{Status: 409, Cause: "OPERATION_UNRESOLVED",
		OperationID: "0199c8a2-0000-7000-8000-000000000001"}}
	w = do(e.h, hx(request("POST", "/subscribers/001020000000001/status", e.user, url.Values{"status": {"suspended"}})))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `<a href="/operations/0199c8a2-0000-7000-8000-000000000001">`) {
		t.Errorf("unresolved = %d %s", w.Code, w.Body)
	}

	// provisioner が状態を返さない（0.2.0 以前）なら、ボタンを出さない。
	sub.Status = ""
	e.pv.subs["001020000000001"] = sub
	body = do(e.h, request("GET", "/subscribers/001020000000001", e.user, nil)).Body.String()
	mustContain(t, body, "本PoCの provisioning-api 0.4.0 以降、eapaka-node-provisioner 0.3.0 以降で使えます")
}
