package provapi

import (
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/certs"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/trace"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// testEnv は mTLS の Provisioning API の代わりになるテスト用サーバーと、それにつながるクライアント。
type testEnv struct {
	client *Client
	srv    *httptest.Server
	// clientFP はクライアントが提示するはずの証明書のフィンガープリント。
	clientFP string
	dir      string
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newTestEnv はハンドラー h を mTLS で提供するサーバーを立て、クライアントを作る。
// provisioning-api と同じく、クライアント証明書は RequestClientCert で求めて VerifyConnection で確かめる。
// クライアント証明書は証明書と秘密鍵を 1 つの PEM にまとめた形（gen-client-cert の標準出力と同じ）で渡す。
func newTestEnv(t *testing.T, h http.HandlerFunc) *testEnv {
	t.Helper()
	dir := t.TempDir()

	serverCertPEM, serverKeyPEM, err := certs.SelfSigned("provisioning-api", []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	clientCertPEM, clientKeyPEM, err := certs.SelfSignedClient("bff-01", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = slog.NewLogLogger(discard.Handler(), slog.LevelWarn)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequestClientCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("client certificate required")
			}
			return nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	writeFile(t, filepath.Join(dir, "client.pem"), append(clientCertPEM, clientKeyPEM...))
	writeFile(t, filepath.Join(dir, "server.pem"), serverCertPEM)
	c, err := New(Options{
		BaseURL:        srv.URL + "/admin/v1",
		ClientCertFile: filepath.Join(dir, "client.pem"),
		ServerCertFile: filepath.Join(dir, "server.pem"),
		Log:            discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{client: c, srv: srv, clientFP: certs.Fingerprint(clientCert.Leaf), dir: dir}
}

// closedAddr は、待ち受けていないことが確かなアドレスを返す。
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.MarshalWrite(w, v)
}

func writeProblem(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func TestStatusOverMTLS(t *testing.T) {
	var gotFP, gotPath, gotTrace string
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFP = certs.Fingerprint(r.TLS.PeerCertificates[0])
		gotTrace = r.Header.Get("X-Trace-ID")
		writeJSON(w, 200, map[string]any{
			"version": "0.2.0", "nodeName": "poc-01", "startedAt": "2026-10-08T00:00:00Z",
			"subscriberCount": 7, "clientCount": 2, "policyCount": 3,
			"futureField": "ignored",
		})
	})
	st, err := env.client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/status" {
		t.Errorf("path = %q", gotPath)
	}
	if gotFP != env.clientFP {
		t.Error("client certificate was not presented")
	}
	// コンテキストにトレースID がなければ、呼び出しごとに採番して送る。
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(gotTrace) {
		t.Errorf("X-Trace-ID = %q", gotTrace)
	}
	want := Status{Version: "0.2.0", NodeName: "poc-01", StartedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
		SubscriberCount: 7, ClientCount: 2, PolicyCount: 3}
	// 0.2.0 は sessionCount を返さない。
	if st != want {
		t.Errorf("status = %+v", st)
	}
	if certs.Fingerprint(env.client.ClientCertificate()) != env.clientFP {
		t.Error("ClientCertificate mismatch")
	}

	// 0.3.0 からは sessionCount を返す（0 件も 0 として読む）。
	for _, n := range []int64{0, 4} {
		env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"version": "0.3.0", "sessionCount": n})
		})
		st, err := env.client.Status(t.Context())
		if err != nil || st.SessionCount == nil || *st.SessionCount != n {
			t.Errorf("sessionCount %d: %+v, %v", n, st, err)
		}
	}
}

func TestTraceID(t *testing.T) {
	var gotTrace string
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotTrace = r.Header.Get("X-Trace-ID")
		w.Header().Set("X-Trace-ID", gotTrace)
		writeProblem(w, 404, `{"status":404,"cause":"USER_NOT_FOUND"}`)
	})
	ctx := trace.With(t.Context(), "0123456789abcdef0123456789abcdef")
	_, err := env.client.GetSubscriber(ctx, "001010000000001")
	if gotTrace != "0123456789abcdef0123456789abcdef" {
		t.Errorf("X-Trace-ID = %q", gotTrace)
	}
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.TraceID != gotTrace {
		t.Errorf("err = %#v", err)
	}
}

func TestOperator(t *testing.T) {
	var calls int
	var gotOperator string
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotOperator = r.Header.Get("X-Operator-Id")
		if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/keys") {
			writeJSON(w, 200, map[string]any{"imsi": "001010000000001"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// 操作者がなければ送らない（変更操作と秘密の値の取得）。
	if err := env.client.DeleteSubscriber(t.Context(), "001010000000001"); !errors.Is(err, ErrNoOperator) {
		t.Errorf("delete without operator: err = %v", err)
	}
	if _, err := env.client.GetSubscriberKeys(t.Context(), "001010000000001"); !errors.Is(err, ErrNoOperator) {
		t.Errorf("keys without operator: err = %v", err)
	}
	if _, err := env.client.GetRADIUSClientSecret(t.Context(), 1); !errors.Is(err, ErrNoOperator) {
		t.Errorf("secret without operator: err = %v", err)
	}
	if _, _, err := env.client.PutPolicy(t.Context(), "001010000000001", PolicyPut{}); !errors.Is(err, ErrNoOperator) {
		t.Errorf("put policy without operator: err = %v", err)
	}
	// 形式に合わない操作者も送らない。
	ctx := WithOperator(t.Context(), "bad user")
	if err := env.client.DeleteSubscriber(ctx, "001010000000001"); !errors.Is(err, ErrInvalidOperator) {
		t.Errorf("invalid operator: err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("requests were sent: %d", calls)
	}

	ctx = WithOperator(t.Context(), "alice@example")
	if err := env.client.DeleteSubscriber(ctx, "001010000000001"); err != nil {
		t.Fatal(err)
	}
	if gotOperator != "alice@example" {
		t.Errorf("X-Operator-Id = %q", gotOperator)
	}

	// 参照系は操作者がなくても送れる。
	if _, err := env.client.GetSubscriber(t.Context(), "001010000000001"); err != nil {
		t.Fatal(err)
	}
	if gotOperator != "" {
		t.Errorf("read without operator: X-Operator-Id = %q", gotOperator)
	}
}

func TestRequestBodies(t *testing.T) {
	type captured struct {
		method, path, ctype, query string
		body                       map[string]any
	}
	var got captured
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		got = captured{method: r.Method, path: r.URL.Path, ctype: r.Header.Get("Content-Type"), query: r.URL.RawQuery}
		if r.Body != nil {
			json.UnmarshalRead(r.Body, &got.body)
		}
		writeJSON(w, 200, map[string]any{})
	})
	ctx := WithOperator(t.Context(), "alice")

	// 加入者の登録: 空の AMF / SQN は送らず Provisioning API の既定値に任せる。
	if _, err := env.client.CreateSubscriber(ctx, SubscriberCreate{
		IMSI: "001010000000001", Ki: "465b5ce8b199b49faa5f0a2ee238a6bc", OPc: "cd63cb71954a9f4e48a5994e37a02baf",
	}); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/admin/v1/subscribers" || got.ctype != "application/json" || len(got.body) != 3 {
		t.Errorf("create subscriber: %+v", got)
	}

	// 加入者の変更: 指定した項目だけを Merge Patch で送る。
	if _, err := env.client.UpdateSubscriber(ctx, "001010000000001", SubscriberUpdate{AMF: new("b9b9")}); err != nil {
		t.Fatal(err)
	}
	if got.method != "PATCH" || got.ctype != "application/merge-patch+json" || len(got.body) != 1 || got.body["amf"] != "b9b9" {
		t.Errorf("update subscriber: %+v", got)
	}

	// RADIUSクライアントの登録: ベンダーは空でも送る。
	if _, err := env.client.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: "192.0.2.1", Secret: "s3cret", Name: "AP-01"}); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/admin/v1/clients" || got.body["vendor"] != "" || len(got.body) != 4 {
		t.Errorf("create client: %+v", got)
	}

	// RADIUSクライアントの変更: 空文字列に変える項目も送る。
	if _, err := env.client.UpdateRADIUSClient(ctx, 3, RADIUSClientUpdate{IP: new("192.0.2.9"), Vendor: new("")}); err != nil {
		t.Fatal(err)
	}
	if got.method != "PATCH" || got.path != "/admin/v1/clients/3" || got.ctype != "application/merge-patch+json" ||
		len(got.body) != 2 || got.body["ip"] != "192.0.2.9" || got.body["vendor"] != "" {
		t.Errorf("update client: %+v", got)
	}

	if _, err := env.client.GetRADIUSClientSecret(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" || got.path != "/admin/v1/clients/3/secret" {
		t.Errorf("client secret: %+v", got)
	}

	// 認可ポリシー: ルールがなくても [] を送る。未設定の VLAN ID と Session-Timeout は送らない。
	if _, _, err := env.client.PutPolicy(ctx, "001010000000001", PolicyPut{Default: PolicyDeny}); err != nil {
		t.Fatal(err)
	}
	if rules, ok := got.body["rules"].([]any); got.method != "PUT" || got.path != "/admin/v1/policies/001010000000001" ||
		got.ctype != "application/json" || !ok || len(rules) != 0 {
		t.Errorf("put policy (no rules): %+v", got)
	}
	if _, _, err := env.client.PutPolicy(ctx, "001010000000001", PolicyPut{Default: PolicyAllow, Rules: []PolicyRule{
		{NASID: "*", AllowedSSIDs: []string{"GUEST"}},
		{NASID: "AP-01", AllowedSSIDs: []string{"CORP", "LAB"}, VLANID: "100", SessionTimeout: 3600},
	}}); err != nil {
		t.Fatal(err)
	}
	rules, _ := got.body["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("put policy: %+v", got)
	}
	if r0 := rules[0].(map[string]any); len(r0) != 2 {
		t.Errorf("rule without vlan/timeout: %v", r0)
	}
	if r1 := rules[1].(map[string]any); r1["vlanId"] != "100" || r1["sessionTimeout"] != float64(3600) {
		t.Errorf("rule with vlan/timeout: %v", r1)
	}

	// クエリ: ゼロ値は送らない。
	env.client.ListSubscribers(t.Context(), ListParams{Prefix: "00101", Limit: 50})
	if got.path != "/admin/v1/subscribers" || got.query != "limit=50&prefix=00101" {
		t.Errorf("list subscribers: %+v", got)
	}
	env.client.ListPolicies(t.Context(), ListParams{Cursor: "abc"})
	if got.path != "/admin/v1/policies" || got.query != "cursor=abc" {
		t.Errorf("list policies: %+v", got)
	}
	env.client.ListRADIUSClients(t.Context())
	if got.path != "/admin/v1/clients" || got.query != "" {
		t.Errorf("list clients: %+v", got)
	}
}

func TestPutPolicyCreated(t *testing.T) {
	status := http.StatusCreated
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, status, map[string]any{
			"imsi": "001010000000001", "default": "deny",
			"rules": []any{map[string]any{"nasId": "*", "allowedSsids": []string{"A"}, "vlanId": "10"}},
		})
	})
	ctx := WithOperator(t.Context(), "alice")
	p, created, err := env.client.PutPolicy(ctx, "001010000000001", PolicyPut{Default: PolicyDeny})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if p.IMSI != "001010000000001" || p.Default != PolicyDeny || len(p.Rules) != 1 ||
		p.Rules[0].VLANID != "10" || !slices.Equal(p.Rules[0].AllowedSSIDs, []string{"A"}) {
		t.Errorf("policy = %+v", p)
	}
	status = http.StatusOK
	if _, created, err := env.client.PutPolicy(ctx, "001010000000001", PolicyPut{Default: PolicyDeny}); err != nil || created {
		t.Errorf("replace: created = %v, err = %v", created, err)
	}
}

func TestFindRADIUSClientByIP(t *testing.T) {
	var gotQuery string
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		items := []any{}
		if r.URL.Query().Get("ip") == "192.0.2.1" {
			items = append(items, map[string]any{"id": 5, "ip": "192.0.2.1", "name": "AP-01", "vendor": ""})
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	c, ok, err := env.client.FindRADIUSClientByIP(t.Context(), "192.0.2.1")
	if err != nil || !ok || c != (RADIUSClient{ID: 5, IP: "192.0.2.1", Name: "AP-01"}) {
		t.Errorf("found: %+v, %v, %v", c, ok, err)
	}
	if gotQuery != "ip=192.0.2.1" {
		t.Errorf("query = %q", gotQuery)
	}
	if _, ok, err := env.client.FindRADIUSClientByIP(t.Context(), "192.0.2.2"); err != nil || ok {
		t.Errorf("not found: %v, %v", ok, err)
	}
}

func TestAuditLogsAndSessions(t *testing.T) {
	var gotPath, gotQuery string
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		switch r.URL.Path {
		case "/admin/v1/audit-logs":
			writeJSON(w, 200, map[string]any{
				"items": []any{map[string]any{
					"id": "1760000000000-1", "time": "2026-10-09T01:02:03.456Z", "operator": "alice", "mgmtClient": "bff-01",
					"action": "subscriber.update", "target": "001010000000001", "targetKey": "sub:001010000000001",
					"traceId": "t1", "details": "amf: 8000 -> b9b9",
				}},
				"nextBefore": "1760000000000-1",
			})
		case "/admin/v1/sessions":
			writeJSON(w, 200, map[string]any{
				"items": []any{
					map[string]any{"id": "u1", "imsi": "001010000000001", "nasIp": "192.0.2.1", "nasIdentifier": "AP-01",
						"startTime": "2026-10-09T01:00:00Z", "clientIp": "10.0.0.5", "acctSessionId": "A1",
						"inputOctets": 100, "outputOctets": 200},
					// 接続開始日時のないセッション（startTime は省略される）。
					map[string]any{"id": "u2", "imsi": "001010000000002", "nasIp": "192.0.2.1", "nasIdentifier": "",
						"clientIp": "", "acctSessionId": "", "inputOctets": 0, "outputOctets": 0},
				},
				"total": 5,
			})
		}
	})

	// 操作者がなくても取得できる（読み出しだけで、provisioning-api の監査ログにも残らない）。
	l, err := env.client.ListAuditLogs(t.Context(), AuditLogParams{Before: "1760000000001-0", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/audit-logs" || gotQuery != "before=1760000000001-0&limit=50" {
		t.Errorf("audit-logs request: %s?%s", gotPath, gotQuery)
	}
	want := AuditLogEntry{
		ID: "1760000000000-1", Time: time.Date(2026, 10, 9, 1, 2, 3, 456e6, time.UTC), Operator: "alice", MgmtClient: "bff-01",
		Action: "subscriber.update", Target: "001010000000001", TargetKey: "sub:001010000000001", TraceID: "t1",
		Details: "amf: 8000 -> b9b9",
	}
	if len(l.Items) != 1 || !l.Items[0].Time.Equal(want.Time) || l.NextBefore != "1760000000000-1" {
		t.Fatalf("audit-logs = %+v", l)
	}
	l.Items[0].Time = want.Time
	if l.Items[0] != want {
		t.Errorf("audit-logs item = %+v", l.Items[0])
	}
	env.client.ListAuditLogs(t.Context(), AuditLogParams{})
	if gotQuery != "" {
		t.Errorf("audit-logs query without params = %q", gotQuery)
	}

	s, err := env.client.ListSessions(t.Context(), SessionParams{IMSI: "001010000000001", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/sessions" || gotQuery != "imsi=001010000000001&limit=1000" {
		t.Errorf("sessions request: %s?%s", gotPath, gotQuery)
	}
	if s.Total != 5 || len(s.Items) != 2 {
		t.Fatalf("sessions = %+v", s)
	}
	if got := s.Items[0]; got.ID != "u1" || got.NasIP != "192.0.2.1" || got.NasIdentifier != "AP-01" ||
		!got.StartTime.Equal(time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)) || got.ClientIP != "10.0.0.5" ||
		got.AcctSessionID != "A1" || got.InputOctets != 100 || got.OutputOctets != 200 {
		t.Errorf("session = %+v", got)
	}
	if !s.Items[1].StartTime.IsZero() {
		t.Errorf("session without startTime = %+v", s.Items[1])
	}
}

func TestErrors(t *testing.T) {
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v1/clients/9":
			writeProblem(w, 404, `{"title":"Not Found","status":404,"cause":"CLIENT_NOT_FOUND"}`)
		case "/admin/v1/policies/001010000000001":
			writeProblem(w, 400, `{"title":"Bad Request","status":400,"cause":"MANDATORY_IE_INCORRECT",`+
				`"invalidParams":[{"param":"rules[0].allowedSsids[1]","reason":"must be 1-32 bytes"}]}`)
		case "/admin/v1/clients":
			writeProblem(w, 409, `{"title":"Conflict","status":409,"cause":"CLIENT_ALREADY_EXISTS"}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := WithOperator(t.Context(), "alice")

	_, err := env.client.GetRADIUSClient(t.Context(), 9)
	if CauseOf(err) != CauseClientNotFound || IsUnavailable(err) {
		t.Errorf("not found: err = %v", err)
	}

	_, err = env.client.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: "192.0.2.1"})
	if CauseOf(err) != CauseClientExists {
		t.Errorf("conflict: err = %v", err)
	}

	_, _, err = env.client.PutPolicy(ctx, "001010000000001", PolicyPut{Default: PolicyDeny})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Status != 400 || apiErr.Problem.Cause != CauseMandatoryIEIncorrect ||
		!slices.Equal(apiErr.Problem.InvalidParams, []InvalidParam{{"rules[0].allowedSsids[1]", "must be 1-32 bytes"}}) {
		t.Errorf("bad request: err = %#v", err)
	}

	// ProblemDetails でない応答もステータスで扱える。
	_, err = env.client.GetPolicy(t.Context(), "001010000000002")
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 404 || apiErr.Problem.Cause != "" {
		t.Errorf("plain 404: err = %v", err)
	}
}

func TestServerCertificateMismatch(t *testing.T) {
	env := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{})
	})
	// 別の自己署名証明書を信頼させると、接続できない。
	otherPEM, _, err := certs.SelfSigned("other", []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.dir, "other.pem"), otherPEM)
	c, err := New(Options{
		BaseURL:        env.srv.URL + "/admin/v1",
		ClientCertFile: filepath.Join(env.dir, "client.pem"),
		ServerCertFile: filepath.Join(env.dir, "other.pem"),
		Log:            discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Status(t.Context())
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok || !IsUnavailable(err) {
		t.Errorf("err = %v", err)
	}

	// 届かない宛先も IsUnavailable。
	c.base.Host = closedAddr(t)
	if _, err := c.Status(t.Context()); !IsUnavailable(err) {
		t.Errorf("unreachable: err = %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	env := newTestEnv(t, func(http.ResponseWriter, *http.Request) {})
	client := filepath.Join(env.dir, "client.pem")
	server := filepath.Join(env.dir, "server.pem")
	for name, opts := range map[string]Options{
		"http url":       {BaseURL: "http://provisioning-api:9444/admin/v1", ClientCertFile: client, ServerCertFile: server},
		"no client cert": {BaseURL: env.srv.URL, ClientCertFile: filepath.Join(env.dir, "none.pem"), ServerCertFile: server},
		"no server cert": {BaseURL: env.srv.URL, ClientCertFile: client, ServerCertFile: filepath.Join(env.dir, "none.pem")},
		"server not pem": {BaseURL: env.srv.URL, ClientCertFile: client, ServerCertFile: client + "x"},
	} {
		if name == "server not pem" {
			writeFile(t, opts.ServerCertFile, []byte("not a pem"))
		}
		if _, err := New(opts); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// 秘密鍵を別ファイルで渡す形も使える（gen-client-cert -out-cert / -out-key の出力）。
	certPEM, keyPEM, err := certs.SelfSignedClient("bff-01", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.dir, "c.pem"), certPEM)
	writeFile(t, filepath.Join(env.dir, "k.pem"), keyPEM)
	if _, err := New(Options{
		BaseURL: env.srv.URL, ClientCertFile: filepath.Join(env.dir, "c.pem"),
		ClientKeyFile: filepath.Join(env.dir, "k.pem"), ServerCertFile: server, Log: discard,
	}); err != nil {
		t.Errorf("separate key file: %v", err)
	}
}

func TestDiagnose(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) }

	// 別の証明書を信頼している。
	env := newTestEnv(t, ok)
	otherPEM, _, err := certs.SelfSigned("other", []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.dir, "server.pem"), otherPEM)
	c, err := New(Options{BaseURL: env.srv.URL, ClientCertFile: filepath.Join(env.dir, "client.pem"),
		ServerCertFile: filepath.Join(env.dir, "server.pem"), Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Status(t.Context())
	if got := Diagnose(err); !strings.Contains(got, "EAPAKA_WEBGUI_ADMIN_SERVER_CERT") {
		t.Errorf("unknown authority: %q (err %v)", got, err)
	}

	// サーバー証明書の SAN に接続先がない。
	env = newTestEnv(t, ok)
	env.client.base.Host = strings.Replace(env.client.base.Host, "127.0.0.1", "localhost", 1)
	_, err = env.client.Status(t.Context())
	if got := Diagnose(err); !strings.Contains(got, "DNS:provisioning-api") {
		t.Errorf("hostname: %q (err %v)", got, err)
	}

	// クライアント証明書を拒否された（provisioning-api と同じく VerifyConnection で拒否する）。
	env = newTestEnv(t, ok)
	env.srv.TLS.VerifyConnection = func(tls.ConnectionState) error {
		return errors.New("client certificate is not a configured admin client")
	}
	_, err = env.client.Status(t.Context())
	if got := Diagnose(err); !strings.Contains(got, "PROVISIONING_API_ADMIN_CLIENTS") {
		t.Errorf("rejected client: %q (err %v)", got, err)
	}

	// 拒否のアラートより先に接続のリセットが届いた（TLS 1.3 で起こりうる）。
	resetErr := &url.Error{Op: "Get", URL: "https://provisioning-api:9444/admin/v1/status",
		Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}}
	if got := Diagnose(resetErr); !strings.Contains(got, "接続を切りました") || !strings.Contains(got, "PROVISIONING_API_ADMIN_CLIENTS") {
		t.Errorf("reset: %q", got)
	}

	// 接続できない。
	env.client.base.Host = closedAddr(t)
	_, err = env.client.Status(t.Context())
	if got := Diagnose(err); !strings.Contains(got, "起動しているか") {
		t.Errorf("dial: %q (err %v)", got, err)
	}

	// 名前を解決できない。
	dnsErr := fmt.Errorf("wrap: %w", &net.DNSError{Name: "provisioning-api", IsNotFound: true})
	if got := Diagnose(dnsErr); !strings.Contains(got, "（provisioning-api）") || !strings.Contains(got, "eapaka-prov") {
		t.Errorf("dns: %q", got)
	}

	if got := Diagnose(&Error{Status: 404}); got != "" {
		t.Errorf("api error: %q", got)
	}
}
