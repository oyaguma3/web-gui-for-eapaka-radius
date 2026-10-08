package web

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
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
	for _, link := range []string{`href="/subscribers"`, `href="/clients"`, `href="/policies"`, `href="/sessions"`} {
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

func TestProvAudit(t *testing.T) {
	e := newScreenEnv(t)
	at := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	for i := range 60 {
		e.prov.auditLogs = append(e.prov.auditLogs, provapi.AuditLogEntry{
			ID: fmt.Sprintf("17600000%05d-0", 99999-i), Time: at, Operator: "alice", MgmtClient: "bff-01",
			Action: "subscriber.update", Target: "001010000000001", TraceID: fmt.Sprintf("trace-%02d", i),
			Details: "amf: 8000 -> b9b9",
		})
	}
	// 操作者のない操作（X-Operator-Id を省略した管理クライアント）は「-」で出す。
	e.prov.auditLogs[1].Operator, e.prov.auditLogs[1].Action = "", "client.secret.read"

	w := do(e.h, request("GET", "/audit/prov", e.owner, nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || e.prov.lastAuditParams != (provapi.AuditLogParams{Limit: auditPerPage}) {
		t.Fatalf("%d %+v", w.Code, e.prov.lastAuditParams)
	}
	for _, want := range []string{
		`<a href="/audit/prov" aria-current="page">provisioning-api</a>`, "<td>alice</td><td>bff-01</td><td>加入者の変更</td><td>001010000000001</td>",
		"<td>-</td><td>bff-01</td><td>共有シークレットの表示</td>", "amf: 8000 -&gt; b9b9", "trace-00", "さらに古いものを表示",
		`hx-get="/audit/prov?before=1760000099950-0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prov audit page does not contain %q", want)
		}
	}
	if n := strings.Count(body, "<tr><td>"); n != auditPerPage {
		t.Errorf("rows = %d", n)
	}

	// 続きは行だけを返す。
	w = do(e.h, hx(request("GET", "/audit/prov?before=1760000099950-0", e.owner, nil)))
	body = w.Body.String()
	if w.Code != http.StatusOK || e.prov.lastAuditParams.Before != "1760000099950-0" || strings.Count(body, "<tr><td>") != 10 ||
		!strings.Contains(body, `hx-swap-oob="beforeend:#audit-rows"`) || strings.Contains(body, "<h1>") || strings.Contains(body, "さらに古いもの") {
		t.Errorf("more: %d %s", w.Code, body)
	}

	// 形式の正しくない続きの位置は Provisioning API に送らない。
	e.prov.lastAuditParams = provapi.AuditLogParams{}
	if w := do(e.h, request("GET", "/audit/prov?before=abc", e.owner, nil)); w.Code != http.StatusBadRequest ||
		e.prov.lastAuditParams != (provapi.AuditLogParams{}) {
		t.Errorf("bad before: %d", w.Code)
	}

	// 一般ユーザーは見られない。BFF のタブからは provisioning-api のタブに移れる。
	if w := do(e.h, request("GET", "/audit/prov", e.user, nil)); w.Code != http.StatusForbidden {
		t.Errorf("user: %d", w.Code)
	}
	if body := do(e.h, request("GET", "/audit", e.owner, nil)).Body.String(); !strings.Contains(body, `<a href="/audit/prov">provisioning-api</a>`) ||
		!strings.Contains(body, `<a href="/audit" aria-current="page">BFF</a>`) {
		t.Error("audit page has no tabs")
	}

	// 記録がない場合。
	e.prov.auditLogs = nil
	if body := do(e.h, request("GET", "/audit/prov", e.owner, nil)).Body.String(); !strings.Contains(body, "記録はありません。") {
		t.Error("empty audit")
	}
}

func TestMonitoringErrors(t *testing.T) {
	e := newScreenEnv(t)
	for name, tc := range map[string]struct {
		err    error
		status int
		want   string
	}{
		// 0.3.0 より前の provisioning-api は、存在しないパスとして 404（cause なし）を返す。
		"old api": {&provapi.Error{Status: 404, Problem: provapi.Problem{Status: 404, Detail: "no such resource"}}, http.StatusBadGateway,
			"に対応していません（provisioning-api 0.3.0 以降が必要です）。"},
		"unreachable": {fmt.Errorf("wrap: %w", &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}), http.StatusBadGateway,
			"provisioning-api に接続できません。"},
		"api error": {&provapi.Error{Status: 500, Problem: provapi.Problem{Cause: provapi.CauseSystemFailure}}, http.StatusBadGateway,
			"本PoCの Provisioning API がエラーを返しました。"},
	} {
		e.prov.monitorErr = tc.err
		for path, what := range map[string]string{"/audit/prov": "監査ログの参照", "/sessions": "セッションの参照"} {
			w := do(e.h, request("GET", path, e.owner, nil))
			want := tc.want
			if name == "old api" {
				want = what + want
			}
			if w.Code != tc.status || !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s %s: %d, want %q", name, path, w.Code, want)
			}
			if strings.Contains(w.Body.String(), "全 0 件") {
				t.Errorf("%s %s: shows a count", name, path)
			}
		}
	}
}
