package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
)

const (
	testKi  = "465b5ce8b199b49faa5f0a2ee238a6bc"
	testOPc = "cd63cb71954a9f4e48a5994e37a02baf"
)

// screenEnv は画面のテスト環境。最初の管理者（root）と一般ユーザー（bob）がログインしている。
type screenEnv struct {
	*testEnv
	owner, user *http.Cookie
}

func newScreenEnv(t *testing.T) *screenEnv {
	t.Helper()
	env := newTestEnv(t, newFakeProv())
	if err := env.auth.CreateAccount(t.Context(), auth.Account{ID: ownerID, Role: auth.RoleOwner}, "bob", auth.RoleUser, "bob-initial-pw"); err != nil {
		t.Fatal(err)
	}
	user := env.loginAs(t, "bob", "bob-initial-pw")
	w := do(env.h, request("POST", "/password", user, url.Values{"current": {"bob-initial-pw"}, "password": {"bob-own-pass"}, "confirm": {"bob-own-pass"}}))
	return &screenEnv{testEnv: env, owner: env.loginAs(t, ownerID, ownerPW), user: sessionCookieOf(w)}
}

func hx(r *http.Request) *http.Request {
	r.Header.Set("HX-Request", "true")
	return r
}

func (e *screenEnv) seed(t *testing.T, imsi string) {
	t.Helper()
	if _, err := e.prov.CreateSubscriber(t.Context(), provapi.SubscriberCreate{
		IMSI: imsi, Ki: testKi, OPc: testOPc, AMF: "8000", SQN: "000000000000",
	}); err != nil {
		t.Fatal(err)
	}
}

// lastAudit は BFF の監査ログの最後の記録を返す。
func (e *screenEnv) lastAudit() store.AuditEntry { return e.store.LastAudit() }

func TestSubscriberList(t *testing.T) {
	e := newScreenEnv(t)
	for i := range 55 {
		e.seed(t, fmt.Sprintf("0010100000%05d", i))
	}
	e.seed(t, "440100123456789")

	body := do(e.h, request("GET", "/subscribers", e.user, nil)).Body.String()
	if !strings.Contains(body, "全 56 件") || !strings.Contains(body, `href="/subscribers/001010000000000"`) ||
		!strings.Contains(body, "cursor=001010000000049") {
		t.Errorf("list page: %s", body)
	}
	body = do(e.h, request("GET", "/subscribers?cursor=001010000000049", e.user, nil)).Body.String()
	if !strings.Contains(body, "440100123456789") || strings.Contains(body, "cursor=") || !strings.Contains(body, "先頭へ") {
		t.Errorf("page 2: %s", body)
	}
	body = do(e.h, request("GET", "/subscribers?prefix=4401", e.user, nil)).Body.String()
	if !strings.Contains(body, "4401 で始まる加入者 1 件") {
		t.Errorf("prefix: %s", body)
	}
	if w := do(e.h, request("GET", "/subscribers?prefix=44a", e.user, nil)); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "15 桁までの数字") {
		t.Errorf("bad prefix: %d", w.Code)
	}
}

func TestSubscriberCreate(t *testing.T) {
	e := newScreenEnv(t)
	form := url.Values{"imsi": {"001010000000001"}, "ki": {strings.ToUpper(testKi)}, "opc": {testOPc}, "amf": {"8000"}, "sqn": {"000000000000"}}

	// 一般ユーザーも登録できる。16 進は小文字にして送る。
	w := do(e.h, request("POST", "/subscribers", e.user, form))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/subscribers/001010000000001?created=1" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if k := e.prov.keys["001010000000001"]; k.Ki != testKi {
		t.Errorf("keys = %+v", k)
	}
	if got := e.prov.lastCall(); got != "CreateSubscriber bob" {
		t.Errorf("call = %q", got)
	}
	if a := e.lastAudit(); a.Actor != "bob" || a.Action != auditSubscriberCreate || a.Target != "001010000000001" ||
		!strings.Contains(a.Detail, `"trace_id":"`) || strings.Contains(a.Detail, testKi) {
		t.Errorf("audit = %+v", a)
	}

	// 重複は 409。Admin TUI など他の操作によるものと分かる文にする。
	w = do(e.h, request("POST", "/subscribers", e.user, form))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "既に登録されています（本PoCの Admin TUI など、他の操作で登録された場合もあります）。") {
		t.Errorf("duplicate: %d", w.Code)
	}

	// 入力の誤りは項目ごとに出し、入力を残す。
	bad := url.Values{"imsi": {"00101"}, "ki": {"xyz"}, "opc": {testOPc}, "amf": {"8000"}, "sqn": {"0"}}
	w = do(e.h, request("POST", "/subscribers", e.user, bad))
	body := w.Body.String()
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "IMSI は 15 桁の数字") ||
		!strings.Contains(body, "Ki は 16 進 32 桁") || !strings.Contains(body, "SQN は 16 進 12 桁") || !strings.Contains(body, `value="00101"`) {
		t.Errorf("invalid: %d %s", w.Code, body)
	}
}

func TestSubscriberDetailAndPermissions(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")

	// 一般ユーザーには Ki / OPc と変更のフォームを出さない。
	body := do(e.h, request("GET", "/subscribers/001010000000001", e.user, nil)).Body.String()
	if strings.Contains(body, "Ki / OPc を表示") || strings.Contains(body, "/auth") || !strings.Contains(body, "この加入者を削除") ||
		!strings.Contains(body, `なし（<a href="/policies/001010000000001">作成する</a>）`) {
		t.Errorf("user view: %s", body)
	}
	for _, path := range []string{"/subscribers/001010000000001/keys", "/subscribers/001010000000001/auth"} {
		if w := do(e.h, request("POST", path, e.user, url.Values{})); w.Code != http.StatusForbidden {
			t.Errorf("user %s: %d", path, w.Code)
		}
	}

	// 管理者には出す。認可ポリシーがあれば、その概要とリンクを出す。
	e.prov.policies["001010000000001"] = provapi.Policy{IMSI: "001010000000001", Default: "deny", Rules: []provapi.PolicyRule{{NASID: "*"}}}
	body = do(e.h, request("GET", "/subscribers/001010000000001", e.owner, nil)).Body.String()
	if !strings.Contains(body, "Ki / OPc を表示") || !strings.Contains(body, `/subscribers/001010000000001/auth`) ||
		!strings.Contains(body, `<a href="/policies/001010000000001">あり</a>（既定 deny、ルール 1 件）`) {
		t.Errorf("owner view: %s", body)
	}

	// Ki / OPc は押したときだけ取得し、監査ログに残す。
	w := do(e.h, hx(request("POST", "/subscribers/001010000000001/keys", e.owner, url.Values{})))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), testKi) || e.prov.lastCall() != "GetSubscriberKeys root" {
		t.Errorf("keys: %d %s", w.Code, w.Body.String())
	}
	if a := e.lastAudit(); a.Action != auditSubscriberKeysRead || strings.Contains(a.Detail, testKi) {
		t.Errorf("audit = %+v", a)
	}

	// 存在しない加入者は 404 と一覧へのリンク。
	w = do(e.h, request("GET", "/subscribers/001010000000099", e.user, nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "他の操作") || !strings.Contains(w.Body.String(), `href="/subscribers"`) {
		t.Errorf("not found: %d %s", w.Code, w.Body.String())
	}
	if w := do(e.h, request("GET", "/subscribers/00101", e.user, nil)); w.Code != http.StatusNotFound {
		t.Errorf("bad imsi: %d", w.Code)
	}
}

func TestSubscriberAuthUpdate(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")
	path := "/subscribers/001010000000001/auth"

	// 変わった項目だけを送る（SQN を変えなければ送らない）。
	w := do(e.h, hx(request("POST", path, e.owner, url.Values{"ki": {""}, "opc": {""}, "amf": {"B9B9"}, "sqn": {"000000000000"}})))
	u := e.prov.lastSubUpdate
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AMF を変更しました。") ||
		u.AMF == nil || *u.AMF != "b9b9" || u.SQN != nil || u.Ki != nil || u.OPc != nil {
		t.Errorf("amf: %d %+v %s", w.Code, u, w.Body.String())
	}
	if a := e.lastAudit(); a.Action != auditSubscriberUpdate || !strings.Contains(a.Detail, `"fields":["amf"]`) {
		t.Errorf("audit = %+v", a)
	}

	w = do(e.h, hx(request("POST", path, e.owner, url.Values{"ki": {strings.Repeat("1", 32)}, "opc": {""}, "amf": {"b9b9"}, "sqn": {"000000000020"}})))
	if !strings.Contains(w.Body.String(), "Ki、SQN を変更しました。") || strings.Contains(e.lastAudit().Detail, strings.Repeat("1", 32)) {
		t.Errorf("ki+sqn: %s / %+v", w.Body.String(), e.lastAudit())
	}

	calls := len(e.prov.calls)
	w = do(e.h, hx(request("POST", path, e.owner, url.Values{"amf": {"b9b9"}, "sqn": {"000000000020"}})))
	if !strings.Contains(w.Body.String(), "変更はありません。") || len(e.prov.calls) != calls {
		t.Errorf("no change: %s", w.Body.String())
	}

	w = do(e.h, hx(request("POST", path, e.owner, url.Values{"ki": {"zz"}, "amf": {"b9b9"}, "sqn": {"000000000020"}})))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Ki は 16 進 32 桁") || len(e.prov.calls) != calls {
		t.Errorf("invalid: %d %s", w.Code, w.Body.String())
	}
}

func TestSubscriberDelete(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")
	e.seed(t, "001010000000002")
	e.prov.policies["001010000000002"] = provapi.Policy{IMSI: "001010000000002", Default: "deny"}

	w := do(e.h, hx(request("POST", "/subscribers/001010000000001/delete", e.user, url.Values{})))
	if w.Header().Get("HX-Redirect") != "/subscribers?deleted=001010000000001" || e.prov.lastCall() != "DeleteSubscriber bob" {
		t.Errorf("delete: %d %v", w.Code, w.Header())
	}
	if a := e.lastAudit(); a.Action != auditSubscriberDelete || a.Target != "001010000000001" {
		t.Errorf("audit = %+v", a)
	}
	body := do(e.h, request("GET", "/subscribers?deleted=001010000000001", e.user, nil)).Body.String()
	if !strings.Contains(body, "加入者 001010000000001 を削除しました。") || strings.Contains(body, "認可ポリシー</a>は残っています") {
		t.Errorf("deleted without policy: %s", body)
	}

	// 同じ IMSI の認可ポリシーは消さず、残っていることを伝える。
	do(e.h, hx(request("POST", "/subscribers/001010000000002/delete", e.user, url.Values{})))
	if _, ok := e.prov.policies["001010000000002"]; !ok {
		t.Error("policy was deleted")
	}
	body = do(e.h, request("GET", "/subscribers?deleted=001010000000002", e.user, nil)).Body.String()
	if !strings.Contains(body, `<a href="/policies/001010000000002">認可ポリシー</a>は残っています`) {
		t.Errorf("deleted with policy: %s", body)
	}

	// 他の操作で既に削除されていた。
	w = do(e.h, hx(request("POST", "/subscribers/001010000000001/delete", e.user, url.Values{})))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "他の操作") || w.Header().Get("HX-Retarget") != "main" {
		t.Errorf("already deleted: %d %v", w.Code, w.Header())
	}
}
