package pvapi

import (
	"context"
	"crypto/x509"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi/provapitest"
)

// recorded は、テスト用サーバーが受け取った要求。
type recorded struct {
	method, path, query, body string
	contentType, operator     string
	idempotencyKey            string
}

// newEnv は、要求を記録して respond の応答を返すテスト用の provisioner と、それにつながるクライアントを作る。
func newEnv(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*Client, *provapi.Client, func() recorded) {
	t.Helper()
	var mu sync.Mutex
	var last recorded
	prov, _ := provapitest.NewServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(b),
			contentType: r.Header.Get("Content-Type"), operator: r.Header.Get("X-Operator-Id"),
			idempotencyKey: r.Header.Get("Idempotency-Key")}
		mu.Unlock()
		respond(w, r)
	}, provapi.Options{Name: Name})
	return New(prov), prov, func() recorded {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func opCtx(t *testing.T) context.Context { return provapi.WithOperator(t.Context(), "alice") }

const statusJSON = `{"version":"0.2.0","startedAt":"2026-10-10T00:00:00Z","plmnMap":[{"plmn":"44020","keyStore":"aka"}],
"downstreams":{"prov":{"configured":true,"url":"https://provisioning-api:9444/admin/v1","reachable":true,"version":"0.3.0","nodeName":"simwifi","subscriberCount":3},
"aka":{"configured":true,"url":"https://aka-only-server:9443/admin/v1","reachable":false,"error":"dial tcp: no such host","hint":"名前を解決できません"}},
"avClient":{"id":1,"exists":true,"enabled":true,"name":"vector-gateway"},"valkey":{"reachable":true},"operations":{"running":0,"retrying":1,"failed":2}}`

func TestStatusAndProbe(t *testing.T) {
	body := statusJSON
	pv, prov, last := newEnv(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, body) })

	st, err := pv.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if last().path != "/admin/v1/status" || st.Version != "0.2.0" || len(st.PLMNMap) != 1 || st.PLMNMap[0].KeyStore != KeyStoreAKA ||
		st.Downstreams.Prov.NodeName != "simwifi" || st.Downstreams.Aka.Reachable || st.Downstreams.Aka.Hint == "" ||
		st.AVClient.Name != "vector-gateway" || st.Operations == nil || st.Operations.Failed != 2 {
		t.Errorf("status = %+v", st)
	}

	// 接続先の判別: provisioner の /status には downstreams、provisioning-api の /status には nodeName がある。
	for _, c := range []struct {
		body string
		want API
	}{
		{statusJSON, APIProvisioner},
		{`{"version":"0.3.0","nodeName":"poc-01","startedAt":"2026-10-10T00:00:00Z","subscriberCount":0}`, APIProvisioningAPI},
		{`{"version":"1.0.0"}`, ""},
	} {
		body = c.body
		if got, err := Probe(t.Context(), prov); err != nil || got != c.want {
			t.Errorf("probe %s = %q, %v; want %q", c.body[:20], got, err, c.want)
		}
	}
}

func TestSubscribers(t *testing.T) {
	pv, _, last := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/admin/v1/subscribers" {
				writeJSON(w, 200, `{"items":[{"imsi":"440200123456789","keyStore":"aka","key":{"amf":"8000","sqn":"000000000020",
"sqnType":"inc32","allowPlain":false,"allowedClientIds":[1],"createdAt":"2026-10-10T00:00:00Z","updatedAt":"2026-10-10T00:00:00Z"},
"policy":{"default":"allow","rules":[]},"issues":[]},{"imsi":"440100000000009","keyStore":"poc","issues":["KEY_MISSING","POLICY_MISSING"]}],"nextCursor":"440100000000009"}`)
				return
			}
			writeJSON(w, 200, `{"imsi":"440100123456789","keyStore":"poc","key":{"amf":"8000","sqn":"000000000000"},"issues":["POLICY_MISSING"]}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			writeJSON(w, 201, `{"imsi":"440100123456789","keyStore":"poc","key":{"amf":"8000","sqn":"000000000000"},"policy":{"default":"deny","rules":[]},"issues":[]}`)
		}
	})
	ctx := opCtx(t)

	list, err := pv.ListSubscribers(ctx, provapi.ListParams{Prefix: "4401", Cursor: "440100000000001", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if q := last().query; q != "cursor=440100000000001&limit=20&prefix=4401" {
		t.Errorf("query = %q", q)
	}
	if len(list.Items) != 2 || list.NextCursor != "440100000000009" || list.Items[0].Key.SQNType != "inc32" ||
		*list.Items[0].Key.AllowPlain || list.Items[0].Key.AllowedClientIDs[0] != 1 || list.Items[0].Policy.Default != "allow" ||
		list.Items[1].Key != nil || list.Items[1].Policy != nil || list.Items[1].Issues[0] != IssueKeyMissing {
		t.Errorf("list = %+v", list)
	}

	sub, err := pv.GetSubscriber(ctx, "440100123456789")
	if err != nil || last().path != "/admin/v1/subscribers/440100123456789" || sub.Policy != nil || sub.Issues[0] != IssuePolicyMissing {
		t.Errorf("get = %+v, %v", sub, err)
	}

	// 作成: 認可ポリシーを含め、省略した項目は送らない。操作者と Idempotency-Key を付ける。
	sub, err = pv.CreateSubscriber(ctx, SubscriberCreate{IMSI: "440100123456789", Ki: "aa", OPc: "bb",
		Policy: provapi.PolicyPut{Default: "deny", Rules: []provapi.PolicyRule{}}}, "key-1")
	if err != nil || sub.Policy.Default != "deny" {
		t.Fatalf("create = %+v, %v", sub, err)
	}
	r := last()
	if r.method != "POST" || r.path != "/admin/v1/subscribers" || r.operator != "alice" || r.idempotencyKey != "key-1" ||
		r.body != `{"imsi":"440100123456789","ki":"aa","opc":"bb","policy":{"default":"deny","rules":[]}}` {
		t.Errorf("create request = %+v", r)
	}

	// 変更: JSON Merge Patch で、指定した項目だけを送る。
	plain := true
	if _, err := pv.UpdateSubscriber(ctx, "440200123456789", SubscriberUpdate{SQNType: "inc33", AllowPlain: &plain}, "key-2"); err != nil {
		t.Fatal(err)
	}
	r = last()
	if r.method != "PATCH" || r.contentType != "application/merge-patch+json" || r.idempotencyKey != "key-2" ||
		r.body != `{"sqnType":"inc33","allowPlain":true}` {
		t.Errorf("update request = %+v", r)
	}

	if err := pv.DeleteSubscriber(ctx, "440200123456789", ""); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "DELETE" || r.path != "/admin/v1/subscribers/440200123456789" || r.idempotencyKey != "" {
		t.Errorf("delete request = %+v", r)
	}

	// 変更操作は、操作者がなければ送らない。
	if err := pv.DeleteSubscriber(t.Context(), "440200123456789", ""); !errors.Is(err, provapi.ErrNoOperator) {
		t.Errorf("delete without operator: %v", err)
	}
}

func TestOperations(t *testing.T) {
	opJSON := `{"id":"0199c8a2-0000-7000-8000-000000000001","kind":"subscriber.create","imsi":"440200123456789","keyStore":"aka",
"status":"retrying","steps":[{"name":"subscriber.create","downstream":"aka","state":"done"},{"name":"policy.put","downstream":"prov","state":"failed",
"error":{"cause":"DOWNSTREAM_UNAVAILABLE","detail":"dial tcp: i/o timeout","time":"2026-10-10T00:00:01Z"}}],"attempts":1,
"nextAttemptAt":"2026-10-10T00:01:00Z","operator":"alice","mgmtClient":"bff","traceId":"t-1","createdAt":"2026-10-10T00:00:00Z","updatedAt":"2026-10-10T00:00:01Z"}`
	pv, _, last := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/operations" {
			writeJSON(w, 200, `{"items":[`+opJSON+`],"total":3}`)
			return
		}
		writeJSON(w, 200, opJSON)
	})
	ctx := opCtx(t)

	list, err := pv.ListOperations(ctx, OpFailed, 10)
	if err != nil || last().query != "limit=10&status=failed" || list.Total != 3 || len(list.Items) != 1 {
		t.Fatalf("list = %+v, %v (query %q)", list, err, last().query)
	}
	op := list.Items[0]
	if op.Status != OpRetrying || op.KeyStore != KeyStoreAKA || op.Attempts != 1 || !op.NextAttemptAt.Equal(time.Date(2026, 10, 10, 0, 1, 0, 0, time.UTC)) ||
		op.Steps[1].Error == nil || op.Steps[1].Error.Cause != CauseDownstreamUnavailable || op.Steps[0].Error != nil {
		t.Errorf("op = %+v", op)
	}
	if _, err := pv.ListOperations(ctx, "", 0); err != nil || last().query != "" {
		t.Errorf("default query = %q, %v", last().query, err)
	}

	if _, err := pv.GetOperation(ctx, op.ID); err != nil || last().path != "/admin/v1/operations/"+op.ID {
		t.Errorf("get: %v (%s)", err, last().path)
	}
	if _, err := pv.RetryOperation(ctx, op.ID, "k-r"); err != nil {
		t.Fatal(err)
	}
	if r := last(); r.method != "POST" || r.path != "/admin/v1/operations/"+op.ID+"/retry" || r.idempotencyKey != "k-r" || r.operator != "alice" {
		t.Errorf("retry request = %+v", r)
	}
	if _, err := pv.DismissOperation(ctx, op.ID, ""); err != nil || last().path != "/admin/v1/operations/"+op.ID+"/dismiss" {
		t.Errorf("dismiss: %v (%s)", err, last().path)
	}
	if _, err := pv.RetryOperation(t.Context(), op.ID, ""); !errors.Is(err, provapi.ErrNoOperator) {
		t.Errorf("retry without operator: %v", err)
	}
}

func TestAuditLogs(t *testing.T) {
	pv, _, last := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v1/audit-logs":
			writeJSON(w, 200, `{"items":[{"id":"2-0","time":"2026-10-10T00:00:02.5Z","operator":"","mgmtClient":"","action":"operation.resume",
"target":"0199c8a2-0000-7000-8000-000000000001","traceId":"t-1","operationId":"0199c8a2-0000-7000-8000-000000000001","result":"rolled_back",
"details":{"imsi":"440200123456789","steps":[{"name":"policy.put","state":"failed"}]}}],"nextBefore":"2-0"}`)
		case "/admin/v1/prov/audit-logs":
			writeJSON(w, 200, `{"items":[{"id":"1-0","time":"2026-10-10T00:00:00Z","operator":"alice","mgmtClient":"provisioner",
"action":"policy.create","target":"440200123456789","targetKey":"policy:440200123456789","traceId":"t-1","details":"default=deny"}]}`)
		case "/admin/v1/aka/audit-logs":
			writeJSON(w, 200, `{"items":[{"id":"1-0","time":"2026-10-10T00:00:00Z","operator":"alice","mgmtClient":"provisioner",
"action":"subscriber.update","target":"440200123456789","traceId":"t-1","detail":{"amf":{"from":"8000","to":"9000"}}}]}`)
		}
	})
	ctx := t.Context()
	p := provapi.AuditLogParams{Before: "3-0", Limit: 50}

	own, err := pv.ListAuditLogs(ctx, p)
	if err != nil || last().query != "before=3-0&limit=50" || own.NextBefore != "2-0" || own.Items[0].Result != "rolled_back" ||
		own.Items[0].OperationID == "" || own.Items[0].Details["imsi"] != "440200123456789" {
		t.Errorf("provisioner = %+v, %v", own, err)
	}
	prov, err := pv.ListProvAuditLogs(ctx, p)
	if err != nil || len(prov.Items) != 1 || prov.Items[0].MgmtClient != "provisioner" || prov.Items[0].TargetKey != "policy:440200123456789" {
		t.Errorf("prov = %+v, %v", prov, err)
	}
	aka, err := pv.ListAkaAuditLogs(ctx, p)
	if err != nil || len(aka.Items) != 1 || fmt.Sprint(aka.Items[0].Detail["amf"]) != "map[from:8000 to:9000]" {
		t.Errorf("aka = %+v, %v", aka, err)
	}
}

func TestProblemExtensions(t *testing.T) {
	pv, _, _ := newEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		json.MarshalWrite(w, map[string]any{"title": "Conflict", "status": 409, "cause": CauseOperationUnresolved,
			"detail": "an unresolved operation (retrying) remains on the same IMSI", "operationId": "0199c8a2-0000-7000-8000-000000000001",
			"downstream": "aka", "conflicts": []string{"poc", "policy"}, "rolledBack": true})
	})
	_, err := pv.CreateSubscriber(opCtx(t), SubscriberCreate{IMSI: "440200123456789"}, "")
	apiErr, ok := errors.AsType[*provapi.Error](err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	p := apiErr.Problem
	if apiErr.Status != 409 || p.Cause != CauseOperationUnresolved || p.OperationID == "" || p.Downstream != "aka" ||
		len(p.Conflicts) != 2 || p.RolledBack == nil || !*p.RolledBack {
		t.Errorf("problem = %+v", p)
	}
	// エラーの文には provisioner と出る（provisioning-api と取り違えないように）。
	if !strings.HasPrefix(err.Error(), "provisioner POST /admin/v1/subscribers: 409") {
		t.Errorf("error = %q", err)
	}
}

func TestDiagnose(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "eapaka-provisioner"}), "PROVISIONER_TLS_HOSTS"},
		{fmt.Errorf("x: %w", x509.UnknownAuthorityError{}), "server-cert"},
		{&net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}, "PROVISIONER_ADMIN_CLIENTS"},
		{fmt.Errorf("x: %w", syscall.ECONNRESET), "admin client certificate rejected"},
		{&net.DNSError{Name: "eapaka-provisioner", IsNotFound: true}, "共有ネットワーク（eapaka-provisioner）"},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, "provisioner が起動しているか"},
		{errors.New("something else"), ""},
	} {
		got := Diagnose(c.err)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("Diagnose(%v) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
}
