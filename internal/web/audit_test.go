package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
)

func TestAudit(t *testing.T) {
	e := newScreenEnv(t)
	// BFF を通した Provisioning API の操作も、ログインやアカウントの操作と並べて出す。
	do(e.h, request("POST", "/subscribers", e.user, url.Values{"imsi": {"001010000000001"}, "ki": {testKi}, "opc": {testOPc}, "amf": {"8000"}, "sqn": {"000000000000"}}))
	body := do(e.h, request("GET", "/audit", e.owner, nil)).Body.String()
	for _, want := range []string{"<td>bob</td>", "<td>加入者の登録</td>", "<td>001010000000001</td>", "trace_id", "<td>ログイン</td>", "<td>パスワードの変更</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page does not contain %q", want)
		}
	}
	if strings.Contains(body, testKi) {
		t.Error("audit page contains Ki")
	}
	if w := do(e.h, request("GET", "/audit", e.user, nil)); w.Code != http.StatusForbidden {
		t.Errorf("user: %d", w.Code)
	}

	// 50 件を超えると「さらに古いものを表示」で続きを読み込む。
	for i := range 60 {
		e.auth.Record(t.Context(), auth.Account{ID: ownerID, Role: auth.RoleOwner}, "policy.delete", fmt.Sprintf("0010100000%05d", i), nil)
	}
	body = do(e.h, request("GET", "/audit", e.owner, nil)).Body.String()
	if !strings.Contains(body, "さらに古いものを表示") || strings.Count(body, "<tr><td>") != auditPerPage {
		t.Errorf("first page rows = %d", strings.Count(body, "<tr><td>"))
	}
	w := do(e.h, hx(request("GET", "/audit?before=20", e.owner, nil)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `hx-swap-oob="beforeend:#audit-rows"`) || strings.Contains(w.Body.String(), "<h1>") {
		t.Errorf("more: %d %s", w.Code, w.Body.String())
	}
}

func TestNavigation(t *testing.T) {
	e := newScreenEnv(t)
	user := do(e.h, request("GET", "/", e.user, nil)).Body.String()
	owner := do(e.h, request("GET", "/", e.owner, nil)).Body.String()
	for _, link := range []string{`href="/subscribers"`, `href="/clients"`, `href="/policies"`} {
		if !strings.Contains(user, link) || !strings.Contains(owner, link) {
			t.Errorf("missing %s", link)
		}
	}
	for _, link := range []string{`href="/audit"`, `href="/accounts"`} {
		if strings.Contains(user, link) || !strings.Contains(owner, link) {
			t.Errorf("admin link %s", link)
		}
	}
}
