package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// pvEnv は eapaka-node-provisioner 経由の画面のテスト環境。最初の管理者（root）と一般ユーザー（bob）がログインしている。
type pvEnv struct {
	*testEnv
	pv          *fakePV
	owner, user *http.Cookie
}

func newPVEnv(t *testing.T) *pvEnv {
	t.Helper()
	pv := newFakePV()
	env := newTestEnvFull(t, newFakeProv(), pv, discard)
	if err := env.auth.CreateAccount(t.Context(), auth.Account{ID: ownerID, Role: auth.RoleOwner}, "bob", auth.RoleUser, "bob-initial-pw"); err != nil {
		t.Fatal(err)
	}
	user := env.loginAs(t, "bob", "bob-initial-pw")
	w := do(env.h, request("POST", "/password", user, url.Values{"current": {"bob-initial-pw"}, "password": {"bob-own-pass"}, "confirm": {"bob-own-pass"}}))
	return &pvEnv{testEnv: env, pv: pv, owner: env.loginAs(t, ownerID, ownerPW), user: sessionCookieOf(w)}
}

func (e *pvEnv) seedPV(imsi string, issues ...pvapi.Issue) {
	ks := keyStoreOf(imsi)
	k := &pvapi.Key{AMF: "8000", SQN: "000000000020", CreatedAt: time.Now()}
	if ks == pvapi.KeyStoreAKA {
		k.SQNType, k.AllowPlain, k.AllowedClientIDs = "inc32", new(false), []int64{1}
	}
	e.pv.subs[imsi] = pvapi.Subscriber{IMSI: imsi, KeyStore: ks, Key: k,
		Policy: &provapi.PolicyPut{Default: "deny", Rules: []provapi.PolicyRule{}}, Issues: append([]pvapi.Issue{}, issues...)}
}

// idemPattern は、フォームに持たせた Idempotency-Key を取り出す。
var idemPattern = regexp.MustCompile(`name="idem" value="([A-Z2-7]{26})"`)

func idemKeys(body string) []string {
	var keys []string
	for _, m := range idemPattern.FindAllStringSubmatch(body, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("body does not contain %q", s)
		}
	}
}

func TestPVDashboardAndNavigation(t *testing.T) {
	e := newPVEnv(t)
	e.pv.status.Downstreams.Aka.Reachable = false
	e.pv.status.Downstreams.Aka.Hint = "aka-only-server のホスト名を解決できません。"
	e.pv.status.Operations = &pvapi.OperationCounts{Retrying: 1, Failed: 2}
	w := do(e.h, request("GET", "/", e.user, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	mustContain(t, body, "eapaka-node-provisioner 0.2.0", "完了していない操作があります（実行中 0、やり直し中 1、失敗 2）",
		"aka-only-server のホスト名を解決できません。", "<code>00102</code> → aka-only-server", `<a href="/operations">操作の記録</a>`,
		"ノード poc-01", "aka-only-server に接続できないため、確かめられませんでした")
	// RADIUSクライアントの件数など、provisioner の /status にないものは出さない。
	if strings.Contains(body, `<a href="/clients">RADIUSクライアント</a></header>`) {
		t.Error("dashboard shows the client count")
	}

	// 直接つなぐ設定では、操作の記録のメニューも画面もない。
	direct := newTestEnv(t, newFakeProv())
	c := direct.loginAs(t, ownerID, ownerPW)
	if body := do(direct.h, request("GET", "/", c, nil)).Body.String(); strings.Contains(body, "/operations") {
		t.Error("direct mode shows operations")
	}
	if w := do(direct.h, request("GET", "/operations", c, nil)); w.Code != http.StatusNotFound {
		t.Errorf("direct /operations = %d", w.Code)
	}
}

func TestPVSubscriberList(t *testing.T) {
	e := newPVEnv(t)
	e.seedPV("001010000000001")
	e.seedPV("001020000000002", pvapi.IssuePolicyMissing)
	e.pv.subs["001010000000003"] = pvapi.Subscriber{IMSI: "001010000000003", KeyStore: pvapi.KeyStorePoC,
		Policy: &provapi.PolicyPut{Default: "allow"}, Issues: []pvapi.Issue{pvapi.IssueKeyMissing}}
	w := do(e.h, request("GET", "/subscribers?deleted=001010000000009", e.user, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	mustContain(t, w.Body.String(), "加入者 001010000000009 を削除しました（認可ポリシーも削除しました）。",
		"<td>aka-only-server</td>", "認可ポリシーがありません", "鍵がありません", "鍵なし", "既定 allow、ルール 0 件")
	if w := do(e.h, request("GET", "/subscribers?cursor=abc", e.user, nil)); w.Code != http.StatusBadRequest {
		t.Errorf("bad cursor = %d", w.Code)
	}
}

func TestPVSubscriberCreate(t *testing.T) {
	e := newPVEnv(t)

	// 登録の画面: PLMN マップと Idempotency-Key を出す。既定は deny で、確認ダイアログはない。
	w := do(e.h, request("GET", "/subscribers/new?imsi=001020000000001", e.user, nil))
	body := w.Body.String()
	mustContain(t, body, `value="001020000000001"`, "<code>00102</code> → aka-only-server", `value="deny" checked`)
	if len(idemKeys(body)) != 1 || strings.Contains(body, "hx-confirm=") {
		t.Errorf("new form: keys %v, confirm %v", idemKeys(body), strings.Contains(body, "hx-confirm="))
	}
	key := idemKeys(body)[0]

	// allow に切り替えるとフォームを描き直し、確認ダイアログを付ける（入力とキーはそのまま）。
	form := url.Values{"imsi": {"001020000000001"}, "ki": {strings.ToUpper(testKi)}, "opc": {testOPc}, "amf": {"8000"},
		"sqn": {"000000000000"}, "default": {"allow"}, "idem": {key}}
	w = do(e.h, hx(request("POST", "/subscribers/new/form", e.user, form)))
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "hx-confirm=") || !strings.Contains(body, `value="allow" checked`) ||
		idemKeys(body)[0] != key || strings.Contains(body, "<html") {
		t.Errorf("switch to allow = %d %s", w.Code, body)
	}
	if e.pv.lastCall() != "" {
		t.Errorf("provisioner was called: %s", e.pv.lastCall())
	}

	// 登録: 認可ポリシー（ルールなし）を含め、操作者と Idempotency-Key を付けて送る。
	w = do(e.h, hx(request("POST", "/subscribers", e.user, form)))
	if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") != "/subscribers/001020000000001?created=1" {
		t.Fatalf("create = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if got := e.pv.lastCall(); got != "CreateSubscriber bob "+key {
		t.Errorf("call = %q", got)
	}
	c := e.pv.lastCreate
	if c.Ki != testKi || c.Policy.Default != "allow" || c.Policy.Rules == nil || len(c.Policy.Rules) != 0 || c.KeyStore != "" {
		t.Errorf("create = %+v", c)
	}
	if a := e.store.LastAudit(); a.Action != auditSubscriberCreate || !strings.Contains(a.Detail, `"keyStore":"aka"`) ||
		!strings.Contains(a.Detail, `"policyDefault":"allow"`) || strings.Contains(a.Detail, testKi) {
		t.Errorf("audit = %+v", a)
	}
	w = do(e.h, request("GET", "/subscribers/001020000000001?created=1", e.user, nil))
	mustContain(t, w.Body.String(), "加入者を登録しました（認可ポリシーも作りました）。", "aka-only-server")

	// 入力の誤りは provisioner に送らない。
	bad := url.Values{"imsi": {"123"}, "ki": {"zz"}, "opc": {testOPc}, "amf": {"8000"}, "sqn": {"000000000000"}, "idem": {key}}
	w = do(e.h, hx(request("POST", "/subscribers", e.user, bad)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "IMSI は 15 桁の数字で入力してください。") {
		t.Errorf("invalid = %d", w.Code)
	}

	// provisioner が 4xx を返したら（未完了の操作がある）、操作の記録へのリンクを出し、キーを新しくする。
	e.pv.errs["CreateSubscriber"] = &provapi.Error{Status: 409, Problem: provapi.Problem{Cause: pvapi.CauseOperationUnresolved,
		OperationID: "0199c8a2-0000-7000-8000-000000000001"}}
	form.Set("imsi", "001010000000005")
	w = do(e.h, hx(request("POST", "/subscribers", e.user, form)))
	body = w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "完了していない操作が残っています") ||
		!strings.Contains(body, `href="/operations/0199c8a2-0000-7000-8000-000000000001"`) || idemKeys(body)[0] == key {
		t.Errorf("unresolved = %d %v %s", w.Code, idemKeys(body), body)
	}
	// provisioner に届かなかったら、同じキーで送り直せるよう残す。
	e.pv.errs["CreateSubscriber"] = fmt.Errorf("provisioner POST /admin/v1/subscribers: %w", errors.New("i/o timeout"))
	w = do(e.h, hx(request("POST", "/subscribers", e.user, form)))
	if w.Code != http.StatusBadGateway || idemKeys(w.Body.String())[0] != key || !strings.Contains(w.Body.String(), "provisioner に接続できません") {
		t.Errorf("unavailable = %d %v %s", w.Code, idemKeys(w.Body.String()), w.Body)
	}
}

func TestPVSubscriberDetailAndUpdate(t *testing.T) {
	e := newPVEnv(t)
	e.seedPV("001020000000001", pvapi.IssueAVClientNotAllowed)
	e.seedPV("001010000000002")

	// 管理者: aka-only-server の加入者は、SQN の増加タイプと平文HTTP の許可も変えられる。
	w := do(e.h, request("GET", "/subscribers/001020000000001", e.owner, nil))
	body := w.Body.String()
	mustContain(t, body, "vector-gateway の AVクライアントを許可していません", "aka-only-server の管理 GUI などで",
		`<option value="inc32" selected>`, `name="allow_plain"`, "Ki / OPc を表示", "鍵の置き場所（aka-only-server）の Ki / OPc を削除します")
	keys := idemKeys(body)
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("keys = %v", keys)
	}
	// 本PoCの加入者には aka-only-server だけの項目を出さない。
	if body := do(e.h, request("GET", "/subscribers/001010000000002", e.owner, nil)).Body.String(); strings.Contains(body, "sqn_type") {
		t.Error("poc subscriber shows sqn_type")
	}
	// 一般ユーザーは鍵の表示・変更ができない。
	body = do(e.h, request("GET", "/subscribers/001020000000001", e.user, nil)).Body.String()
	if strings.Contains(body, "Ki / OPc を表示") || strings.Contains(body, "/auth") {
		t.Error("user can edit keys")
	}
	if w := do(e.h, hx(request("POST", "/subscribers/001020000000001/auth", e.user, url.Values{}))); w.Code != http.StatusForbidden {
		t.Errorf("user auth = %d", w.Code)
	}

	// 変わった項目だけを送る。
	form := url.Values{"amf": {"8000"}, "sqn": {"000000000020"}, "sqn_type": {"inc33"}, "allow_plain": {"1"}, "idem": {keys[0]}}
	w = do(e.h, hx(request("POST", "/subscribers/001020000000001/auth", e.owner, form)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "SQN の増加タイプ、平文HTTP の許可 を変更しました。") {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	if u := e.pv.lastUpdate; u.SQNType != "inc33" || u.AllowPlain == nil || !*u.AllowPlain || u.AMF != "" || u.SQN != "" || u.Ki != "" {
		t.Errorf("update = %+v", u)
	}
	if got := e.pv.lastCall(); got != "UpdateSubscriber root "+keys[0] {
		t.Errorf("call = %q", got)
	}
	if a := e.store.LastAudit(); a.Action != auditSubscriberUpdate || !strings.Contains(a.Detail, `"sqnType"`) {
		t.Errorf("audit = %+v", a)
	}
	// 変更がなければ送らない。
	before := e.pv.lastCall()
	form = url.Values{"amf": {"8000"}, "sqn": {"000000000020"}, "sqn_type": {"inc33"}, "allow_plain": {"1"}}
	if w := do(e.h, hx(request("POST", "/subscribers/001020000000001/auth", e.owner, form))); !strings.Contains(w.Body.String(), "変更はありません。") ||
		e.pv.lastCall() != before {
		t.Errorf("no change = %s", w.Body)
	}
	// 鍵がない加入者は変更できない。
	e.pv.subs["001010000000003"] = pvapi.Subscriber{IMSI: "001010000000003", KeyStore: pvapi.KeyStorePoC, Issues: []pvapi.Issue{pvapi.IssueKeyMissing}}
	if w := do(e.h, hx(request("POST", "/subscribers/001010000000003/auth", e.owner, form))); w.Code != http.StatusConflict {
		t.Errorf("no key = %d", w.Code)
	}
}

func TestPVSubscriberDelete(t *testing.T) {
	e := newPVEnv(t)
	e.seedPV("001010000000001")
	w := do(e.h, hx(request("POST", "/subscribers/001010000000001/delete", e.user, url.Values{"idem": {"AAAAAAAAAAAAAAAAAAAAAAAAAA"}})))
	if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") != "/subscribers?deleted=001010000000001" {
		t.Fatalf("delete = %d %v", w.Code, w.Header())
	}
	if got := e.pv.lastCall(); got != "DeleteSubscriber bob AAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("call = %q", got)
	}

	// 途中で失敗して元に戻せず、加入者を読み直せない場合も、元の失敗と操作の記録へのリンクを出す。
	e.seedPV("001010000000002")
	e.pv.errs["DeleteSubscriber"] = &provapi.Error{Status: 500, Problem: provapi.Problem{Cause: pvapi.CauseOperationIncomplete,
		Downstream: "aka", OperationID: "0199c8a2-0000-7000-8000-000000000002"}}
	e.pv.errs["GetSubscriber"] = &provapi.Error{Status: 503, Problem: provapi.Problem{Cause: pvapi.CauseDownstreamUnavailable, Downstream: "aka"}}
	w = do(e.h, hx(request("POST", "/subscribers/001010000000002/delete", e.user, url.Values{})))
	body := w.Body.String()
	if w.Code != http.StatusInternalServerError || !strings.Contains(body, "aka-only-server への操作が途中で失敗し") ||
		!strings.Contains(body, `href="/operations/0199c8a2-0000-7000-8000-000000000002"`) {
		t.Errorf("incomplete = %d %s", w.Code, body)
	}
}

func TestOperationsScreens(t *testing.T) {
	e := newPVEnv(t)
	failed := pvapi.Operation{ID: "0199c8a2-0000-7000-8000-000000000001", Kind: "subscriber.delete", IMSI: "001020000000001",
		KeyStore: pvapi.KeyStoreAKA, Status: pvapi.OpFailed, Attempts: 3, Operator: "bob", MgmtClient: "bff", TraceID: "t-1",
		Steps: []pvapi.OperationStep{
			{Name: "policy.delete", Downstream: "prov", State: "done"},
			{Name: "subscriber.delete", Downstream: "aka", State: "failed", Error: &pvapi.StepError{Cause: pvapi.CauseDownstreamUnavailable, Detail: "dial tcp: i/o timeout"}},
		}}
	unknown := pvapi.Operation{ID: "0199c8a2-0000-7000-8000-000000000002", Kind: "subscriber.update", IMSI: "001010000000002",
		KeyStore: pvapi.KeyStorePoC, Status: pvapi.OpFailed, Steps: []pvapi.OperationStep{
			{Name: "policy.put", Downstream: "prov", State: "done"}, {Name: "subscriber.update", Downstream: "prov", State: "pending"}}}
	retrying := pvapi.Operation{ID: "0199c8a2-0000-7000-8000-000000000003", Kind: "subscriber.create", IMSI: "001010000000003",
		Status: pvapi.OpRetrying, NextAttemptAt: time.Now().Add(time.Minute)}
	for _, op := range []pvapi.Operation{failed, unknown, retrying} {
		e.pv.ops[op.ID] = op
	}

	// 一覧と絞り込み（一般ユーザーも見られる）。
	body := do(e.h, request("GET", "/operations", e.user, nil)).Body.String()
	mustContain(t, body, "3 件", "加入者の削除", "失敗（手での対応が必要）", "やり直し中", `href="/operations/`+failed.ID+`"`)
	body = do(e.h, request("GET", "/operations?status=retrying", e.user, nil)).Body.String()
	if !strings.Contains(body, "1 件") || strings.Contains(body, failed.ID) {
		t.Errorf("filter = %s", body)
	}
	if w := do(e.h, request("GET", "/operations?status=done", e.user, nil)); w.Code != http.StatusBadRequest {
		t.Errorf("bad filter = %d", w.Code)
	}

	// 詳細: 一般ユーザーには対応のボタンを出さず、操作もできない。
	body = do(e.h, request("GET", "/operations/"+failed.ID, e.user, nil)).Body.String()
	mustContain(t, body, "認可ポリシーの削除", "加入者（鍵）の削除", "dial tcp: i/o timeout", "やり直しと閉じるは、管理者が行います。")
	if w := do(e.h, hx(request("POST", "/operations/"+failed.ID+"/retry", e.user, url.Values{}))); w.Code != http.StatusForbidden {
		t.Errorf("user retry = %d", w.Code)
	}

	// 管理者: やり直す・閉じる。
	body = do(e.h, request("GET", "/operations/"+failed.ID, e.owner, nil)).Body.String()
	keys := idemKeys(body)
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
	w := do(e.h, hx(request("POST", "/operations/"+failed.ID+"/retry", e.owner, url.Values{"idem": {keys[0]}})))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "やり直しました。操作は「完了」になりました。") || e.pv.lastCall() != "RetryOperation root "+keys[0] {
		t.Errorf("retry = %d %s (%s)", w.Code, w.Body, e.pv.lastCall())
	}
	if a := e.store.LastAudit(); a.Action != auditOperationRetry || a.Target != failed.ID || !strings.Contains(a.Detail, `"status":"completed"`) {
		t.Errorf("audit = %+v", a)
	}
	// やり直しがまた失敗したら、その旨を出す。
	e.pv.ops[failed.ID] = failed
	e.pv.retryResult = pvapi.OpRetrying
	w = do(e.h, hx(request("POST", "/operations/"+failed.ID+"/retry", e.owner, url.Values{})))
	mustContain(t, w.Body.String(), "やり直しましたが、また失敗しました。")

	// 鍵の変更が分からない変更は、やり直すを出さず、閉じるだけを出す。
	body = do(e.h, request("GET", "/operations/"+unknown.ID, e.owner, nil)).Body.String()
	mustContain(t, body, "鍵の変更が反映されたかどうか分かりません", "閉じる")
	if strings.Contains(body, "/retry") {
		t.Error("unknown key change offers retry")
	}
	w = do(e.h, hx(request("POST", "/operations/"+unknown.ID+"/dismiss", e.owner, url.Values{})))
	if !strings.Contains(w.Body.String(), "操作を閉じました。") || e.pv.ops[unknown.ID].Status != pvapi.OpDismissed {
		t.Errorf("dismiss = %s", w.Body)
	}

	// ないもの、形式の違う ID。
	if w := do(e.h, request("GET", "/operations/0199c8a2-0000-7000-8000-00000000000f", e.user, nil)); w.Code != http.StatusNotFound ||
		!strings.Contains(w.Body.String(), "7 日で消えます") {
		t.Errorf("missing = %d", w.Code)
	}
	if w := do(e.h, request("GET", "/operations/x", e.user, nil)); w.Code != http.StatusNotFound {
		t.Errorf("bad id = %d", w.Code)
	}
}

func TestPVAudit(t *testing.T) {
	e := newPVEnv(t)
	e.pv.audit = []pvapi.AuditLogEntry{{ID: "1-0", Time: time.Now(), Operator: "bob", MgmtClient: "bff", Action: "subscriber.create",
		Target: "001010000000001", TraceID: "t-1", OperationID: "0199c8a2-0000-7000-8000-000000000001", Result: "rolled_back",
		Details: map[string]any{"keyStore": "poc"}}}
	e.pv.provAudit = []provapi.AuditLogEntry{{ID: "1-0", Time: time.Now(), MgmtClient: "provisioner", Action: "policy.create", Target: "001010000000001"}}
	e.pv.akaAudit = []pvapi.AkaAuditLogEntry{{ID: "1-0", Time: time.Now(), MgmtClient: "provisioner", Action: "subscriber.update",
		Target: "001020000000001", Detail: map[string]any{"amf": map[string]any{"from": "8000", "to": "9000"}}}}

	body := do(e.h, request("GET", "/audit", e.owner, nil)).Body.String()
	mustContain(t, body, `href="/audit/provisioner"`, `href="/audit/aka"`)
	body = do(e.h, request("GET", "/audit/provisioner", e.owner, nil)).Body.String()
	mustContain(t, body, "元に戻した", `href="/operations/0199c8a2-0000-7000-8000-000000000001"`, "keyStore: poc")
	// provisioning-api のタブは provisioner が中継する監査ログを見る。
	body = do(e.h, request("GET", "/audit/prov", e.owner, nil)).Body.String()
	mustContain(t, body, "<td>provisioner</td>")
	body = do(e.h, request("GET", "/audit/aka", e.owner, nil)).Body.String()
	mustContain(t, body, "amf: 8000 → 9000")
	if w := do(e.h, request("GET", "/audit/aka", e.user, nil)); w.Code != http.StatusForbidden {
		t.Errorf("user aka audit = %d", w.Code)
	}

	// aka-only-server を扱わない設定なら、そのタブは出さない（状態の写しは 1 分使い回す）。
	e2 := newPVEnv(t)
	e2.pv.status.Downstreams.Aka = pvapi.DownstreamStatus{}
	body = do(e2.h, request("GET", "/audit", e2.owner, nil)).Body.String()
	if strings.Contains(body, `href="/audit/aka"`) || !strings.Contains(body, `href="/audit/provisioner"`) {
		t.Errorf("tabs without aka = %s", body)
	}
	calls := e2.pv.statusCalls
	do(e2.h, request("GET", "/audit/prov", e2.owner, nil))
	if e2.pv.statusCalls != calls {
		t.Errorf("status was fetched again: %d -> %d", calls, e2.pv.statusCalls)
	}
}

func TestPVPolicySubscriberState(t *testing.T) {
	e := newPVEnv(t)
	// 認可ポリシーだけがある（鍵がない）IMSI は、鍵が登録されていないと出す。
	e.pv.subs["001010000000001"] = pvapi.Subscriber{IMSI: "001010000000001", KeyStore: pvapi.KeyStorePoC,
		Policy: &provapi.PolicyPut{Default: "deny"}, Issues: []pvapi.Issue{pvapi.IssueKeyMissing}}
	body := do(e.h, request("GET", "/policies/001010000000001", e.user, nil)).Body.String()
	mustContain(t, body, "鍵（Ki / OPc）が登録されていません")
	e.seedPV("001010000000002")
	body = do(e.h, request("GET", "/policies/001010000000002", e.user, nil)).Body.String()
	mustContain(t, body, `<a href="/subscribers/001010000000002">登録あり</a>`)

	// 未完了の操作で断られたら、操作の記録へのリンクを出す。
	e.prov.putPolicyErr = &provapi.Error{Status: 409, Problem: provapi.Problem{Cause: pvapi.CauseOperationUnresolved,
		OperationID: "0199c8a2-0000-7000-8000-000000000009"}}
	w := do(e.h, hx(request("POST", "/policies/001010000000002", e.user, policyForm("deny"))))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `href="/operations/0199c8a2-0000-7000-8000-000000000009"`) {
		t.Errorf("unresolved = %d %s", w.Code, w.Body)
	}
}

func TestPVErrorMessages(t *testing.T) {
	h := &Handler{pv: newFakePV()}
	for _, c := range []struct {
		err        error
		status     int
		want, opID string
	}{
		{&provapi.Error{Status: 409, Problem: provapi.Problem{Cause: pvapi.CauseOperationInProgress}}, 409, "処理中です", ""},
		{&provapi.Error{Status: 503, Problem: provapi.Problem{Cause: pvapi.CauseDownstreamUnavailable, Downstream: "prov",
			OperationID: "op-1", RolledBack: new(true)}}, 503, "provisioner から本PoCの Provisioning API に接続できません。途中まで行った変更は元に戻しました。", "op-1"},
		{&provapi.Error{Status: 502, Problem: provapi.Problem{Cause: pvapi.CauseDownstreamError, Downstream: "aka", DownstreamCause: "CLIENT_NOT_FOUND"}},
			502, "aka-only-server がエラーを返しました（CLIENT_NOT_FOUND）。", ""},
		{&provapi.Error{Status: 409, Problem: provapi.Problem{Cause: provapi.CauseSubscriberExists, Conflicts: []string{"aka", "policy"}}},
			409, "（aka-only-server の鍵、認可ポリシー）", ""},
		{&provapi.Error{Status: 409, Problem: provapi.Problem{Cause: pvapi.CauseOperationStateConflict}}, 409, "鍵の変更が反映されたか分からない変更", ""},
		{&provapi.Error{Status: 500, Problem: provapi.Problem{Cause: "SYSTEM_FAILURE"}}, 502, "provisioner がエラーを返しました。", ""},
	} {
		f := h.apiError(c.err, "")
		if f.Status != c.status || !strings.Contains(f.Message, c.want) || f.OperationID != c.opID {
			t.Errorf("apiError(%v) = %+v", c.err, f)
		}
	}
}
