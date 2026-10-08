package provapi

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/certs"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/trace"
)

// 実際の provisioning-api（本PoC）を相手にした契約テスト。
// 次の環境変数を指定したときだけ実行する。
//
//	EAPAKA_WEBGUI_TEST_ADMIN_URL          Provisioning API のベース URL（例: https://127.0.0.1:19444/admin/v1）
//	EAPAKA_WEBGUI_TEST_ADMIN_CLIENT_CERT  BFF のクライアント証明書の PEM（秘密鍵も含めてよい）
//	EAPAKA_WEBGUI_TEST_ADMIN_CLIENT_KEY   秘密鍵の PEM（証明書のファイルに含めた場合は不要）
//	EAPAKA_WEBGUI_TEST_ADMIN_SERVER_CERT  provisioning-api のサーバー証明書の PEM
//	EAPAKA_WEBGUI_TEST_ADMIN_LOG          provisioning-api の標準出力を書いたファイル（任意）。
//	                                      指定すると、監査ログの操作者・管理クライアント・トレースID も確かめる
//
// テスト用の加入者・認可ポリシー（IMSI 00101 で始まるテスト用の番号）と、RADIUSクライアント
// （文書用のアドレス 198.51.100.0/24）を作り、終わったら削除する。

const integrationOperator = "it-alice"

func newIntegrationClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_URL")
	if url == "" {
		t.Skip("EAPAKA_WEBGUI_TEST_ADMIN_URL is not set")
	}
	c, err := New(Options{
		BaseURL:        url,
		ClientCertFile: os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_CLIENT_CERT"),
		ClientKeyFile:  os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_CLIENT_KEY"),
		ServerCertFile: os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_SERVER_CERT"),
		Log:            discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// opCtx は、操作者と新しいトレースID を入れたコンテキストと、そのトレースID を返す。
func opCtx(t *testing.T) (context.Context, string) {
	id := trace.New()
	return trace.With(WithOperator(t.Context(), integrationOperator), id), id
}

// testIMSI はテスト用の IMSI（00101 で始まる 15 桁）を作る。
func testIMSI() string {
	return fmt.Sprintf("00101%010d", rand.Int64N(1e10))
}

// testIP は文書用のアドレス（198.51.100.0/24）からテスト用の IP アドレスを選ぶ。
// 既に使われているアドレス（前回の失敗で残ったものなど）は避ける。
func testIP(t *testing.T, c *Client, avoid ...string) string {
	t.Helper()
	for range 50 {
		ip := fmt.Sprintf("198.51.100.%d", 1+rand.IntN(254))
		if slices.Contains(avoid, ip) {
			continue
		}
		if _, used, err := c.FindRADIUSClientByIP(t.Context(), ip); err != nil {
			t.Fatal(err)
		} else if !used {
			return ip
		}
	}
	t.Fatal("no free test IP address")
	return ""
}

// auditEntry は provisioning-api の監査ログの 1 行。
type auditEntry struct {
	Msg        string `json:"msg"`
	EventID    string `json:"event_id"`
	TraceID    string `json:"trace_id"`
	Operation  string `json:"operation"`
	TargetType string `json:"target_type"`
	TargetKey  string `json:"target_key"`
	TargetID   string `json:"target_id"`
	TargetIMSI string `json:"target_imsi"`
	AdminUser  string `json:"admin_user"`
	MgmtClient string `json:"mgmt_client"`
	Details    string `json:"details"`
}

// checkAudit は、トレースID traceID の操作が、監査ログに msg として操作者・管理クライアントつきで記録されたことを確かめる。
// EAPAKA_WEBGUI_TEST_ADMIN_LOG が指定されていなければ何もしない。見つかった行を返す。
func checkAudit(t *testing.T, traceID, msg string) auditEntry {
	t.Helper()
	path := os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_LOG")
	if path == "" {
		return auditEntry{}
	}
	// 監査ログは応答の前に書かれるが、念のため少し待って読み直す。
	for range 20 {
		if e, ok := findAudit(t, path, traceID); ok {
			if e.Msg != msg || e.AdminUser != integrationOperator || e.MgmtClient == "" {
				t.Errorf("audit for %s: %+v", msg, e)
			}
			return e
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("audit %q (trace_id %s) not found in %s", msg, traceID, path)
	return auditEntry{}
}

func findAudit(t *testing.T, path, traceID string) (auditEntry, bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e auditEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.EventID == "AUDIT_LOG" && e.TraceID == traceID {
			return e, true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return auditEntry{}, false
}

// logContains は、provisioning-api のログに s を含む行があるかを返す（ログを指定しなければ true）。
func logContains(t *testing.T, s string) bool {
	t.Helper()
	path := os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_LOG")
	if path == "" {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(data), s)
}

// cleanup は、テストの終わりに f を実行する。既に削除済み（404）なら何もしない。
// t.Context() は後片付けの前にキャンセルされるので、ここでは使わない。
func cleanup(t *testing.T, what string, f func(ctx context.Context) error) {
	t.Cleanup(func() {
		err := f(WithOperator(context.Background(), "it-cleanup"))
		if apiErr, ok := errors.AsType[*Error](err); err != nil && (!ok || apiErr.Status != 404) {
			t.Errorf("cleanup %s: %v", what, err)
		}
	})
}

func TestIntegrationStatus(t *testing.T) {
	c := newIntegrationClient(t)
	st, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version == "" || st.NodeName == "" || st.StartedAt.IsZero() || st.StartedAt.After(time.Now()) ||
		st.SubscriberCount < 0 || st.ClientCount < 0 || st.PolicyCount < 0 {
		t.Errorf("status = %+v", st)
	}
	t.Logf("provisioning-api %s (node %s)", st.Version, st.NodeName)
}

func TestIntegrationSubscribers(t *testing.T) {
	c := newIntegrationClient(t)
	imsi := testIMSI()
	cleanup(t, "subscriber "+imsi, func(ctx context.Context) error { return c.DeleteSubscriber(ctx, imsi) })

	before, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// 登録（AMF / SQN は既定値に任せる）。16進の大文字も受け付ける。
	ctx, tid := opCtx(t)
	created, err := c.CreateSubscriber(ctx, SubscriberCreate{
		IMSI: imsi, Ki: "465B5CE8B199B49FAA5F0A2EE238A6BC", OPc: "cd63cb71954a9f4e48a5994e37a02baf",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.IMSI != imsi || created.AMF != "8000" || created.SQN != "000000000000" || created.CreatedAt.IsZero() {
		t.Errorf("created = %+v", created)
	}
	if e := checkAudit(t, tid, "subscriber created"); e.Msg != "" && (e.TargetIMSI != imsi || e.TargetKey != "sub:"+imsi) {
		t.Errorf("audit create = %+v", e)
	}

	// 同じ IMSI はもう登録できない。
	ctx, _ = opCtx(t)
	_, err = c.CreateSubscriber(ctx, SubscriberCreate{IMSI: imsi, Ki: strings.Repeat("0", 32), OPc: strings.Repeat("0", 32)})
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 409 || apiErr.Problem.Cause != CauseSubscriberExists {
		t.Errorf("duplicate: err = %v", err)
	}

	// 入力の誤りは項目つきで返る。
	_, err = c.CreateSubscriber(ctx, SubscriberCreate{IMSI: imsi, Ki: "zz", OPc: strings.Repeat("0", 32)})
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 400 ||
		!slices.ContainsFunc(apiErr.Problem.InvalidParams, func(p InvalidParam) bool { return p.Param == "ki" }) {
		t.Errorf("invalid ki: err = %#v", err)
	}

	got, err := c.GetSubscriber(t.Context(), imsi)
	if err != nil || got != created {
		t.Fatalf("get: %+v, %v", got, err)
	}

	// 一覧（前方一致）と件数。
	list, err := c.ListSubscribers(t.Context(), ListParams{Prefix: imsi, Limit: 10})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].IMSI != imsi || list.NextCursor != "" {
		t.Errorf("list: %+v, %v", list, err)
	}
	if after, err := c.Status(t.Context()); err != nil || after.SubscriberCount < before.SubscriberCount+1 {
		t.Errorf("status count: before %d, after %+v, %v", before.SubscriberCount, after, err)
	}

	// Ki / OPc は小文字で返り、取得は監査ログに残る。
	ctx, tid = opCtx(t)
	keys, err := c.GetSubscriberKeys(ctx, imsi)
	if err != nil || keys != (SubscriberKeys{Ki: "465b5ce8b199b49faa5f0a2ee238a6bc", OPc: "cd63cb71954a9f4e48a5994e37a02baf"}) {
		t.Errorf("keys: %+v, %v", keys, err)
	}
	if e := checkAudit(t, tid, "subscriber secret read"); e.Msg != "" && e.Details != "ki,opc" {
		t.Errorf("audit keys = %+v", e)
	}

	// 変更: AMF だけを変えると SQN はそのまま。
	ctx, tid = opCtx(t)
	updated, err := c.UpdateSubscriber(ctx, imsi, SubscriberUpdate{AMF: new("B9B9")})
	if err != nil || updated.AMF != "b9b9" || updated.SQN != "000000000000" {
		t.Errorf("update amf: %+v, %v", updated, err)
	}
	if e := checkAudit(t, tid, "subscriber updated"); e.Msg != "" && e.Details != "amf: 8000 -> b9b9" {
		t.Errorf("audit update = %+v", e)
	}
	ctx, _ = opCtx(t)
	updated, err = c.UpdateSubscriber(ctx, imsi, SubscriberUpdate{SQN: new("000000000020"), Ki: new(strings.Repeat("1", 32))})
	if err != nil || updated.AMF != "b9b9" || updated.SQN != "000000000020" {
		t.Errorf("update sqn: %+v, %v", updated, err)
	}
	if keys, err := c.GetSubscriberKeys(ctx, imsi); err != nil || keys.Ki != strings.Repeat("1", 32) {
		t.Errorf("keys after update: %+v, %v", keys, err)
	}

	// 削除。もう一度削除すると 404。
	ctx, tid = opCtx(t)
	if err := c.DeleteSubscriber(ctx, imsi); err != nil {
		t.Fatal(err)
	}
	checkAudit(t, tid, "subscriber deleted")
	ctx, _ = opCtx(t)
	for name, err := range map[string]error{
		"delete": c.DeleteSubscriber(ctx, imsi),
		"get":    func() error { _, err := c.GetSubscriber(t.Context(), imsi); return err }(),
		"update": func() error { _, err := c.UpdateSubscriber(ctx, imsi, SubscriberUpdate{AMF: new("8000")}); return err }(),
	} {
		if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 404 || apiErr.Problem.Cause != CauseUserNotFound {
			t.Errorf("%s after delete: err = %v", name, err)
		}
	}
}

func TestIntegrationRADIUSClients(t *testing.T) {
	c := newIntegrationClient(t)
	ip := testIP(t, c)

	ctx, tid := opCtx(t)
	created, err := c.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: ip, Secret: "it-secret!", Name: "it-ap-01", Vendor: "generic"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, "client "+ip, func(ctx context.Context) error { return c.DeleteRADIUSClient(ctx, created.ID) })
	if created.ID < 1 || created.IP != ip || created.Name != "it-ap-01" || created.Vendor != "generic" {
		t.Errorf("created = %+v", created)
	}
	if e := checkAudit(t, tid, "client created"); e.Msg != "" && e.TargetID != fmt.Sprint(created.ID) {
		t.Errorf("audit create = %+v", e)
	}

	// 同じ IP はもう登録できない。
	ctx, _ = opCtx(t)
	_, err = c.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: ip, Secret: "x", Name: "it-ap-02"})
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 409 || apiErr.Problem.Cause != CauseClientExists {
		t.Errorf("duplicate: err = %v", err)
	}
	// 入力の誤りは項目つきで返る。
	_, err = c.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: "198.51.100.300", Secret: "x", Name: "bad name"})
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 400 || len(apiErr.Problem.InvalidParams) < 2 {
		t.Errorf("invalid: err = %#v", err)
	}

	if got, err := c.GetRADIUSClient(t.Context(), created.ID); err != nil || got != created {
		t.Errorf("get: %+v, %v", got, err)
	}
	if got, ok, err := c.FindRADIUSClientByIP(t.Context(), ip); err != nil || !ok || got != created {
		t.Errorf("find: %+v, %v, %v", got, ok, err)
	}
	all, err := c.ListRADIUSClients(t.Context())
	if err != nil || !slices.Contains(all, created) {
		t.Errorf("list: %+v, %v", all, err)
	}

	// 共有シークレットの取得は監査ログに残る。
	ctx, tid = opCtx(t)
	if secret, err := c.GetRADIUSClientSecret(ctx, created.ID); err != nil || secret != "it-secret!" {
		t.Errorf("secret: %q, %v", secret, err)
	}
	if e := checkAudit(t, tid, "client secret read"); e.Msg != "" && e.Details != "secret" {
		t.Errorf("audit secret = %+v", e)
	}

	// IP を変えても ID は変わらない。ベンダーは空にできる。
	newIP := testIP(t, c, ip)
	ctx, tid = opCtx(t)
	updated, err := c.UpdateRADIUSClient(ctx, created.ID, RADIUSClientUpdate{IP: new(newIP), Vendor: new(""), Secret: new("it-secret-2")})
	if err != nil || updated != (RADIUSClient{ID: created.ID, IP: newIP, Name: "it-ap-01", Vendor: ""}) {
		t.Errorf("update: %+v, %v", updated, err)
	}
	if e := checkAudit(t, tid, "client updated"); e.Msg != "" &&
		(e.TargetID != fmt.Sprint(created.ID) || e.TargetKey != "client:"+newIP) {
		t.Errorf("audit update = %+v", e)
	}
	if _, ok, err := c.FindRADIUSClientByIP(t.Context(), ip); err != nil || ok {
		t.Errorf("find old ip: %v, %v", ok, err)
	}
	ctx, _ = opCtx(t)
	if secret, err := c.GetRADIUSClientSecret(ctx, created.ID); err != nil || secret != "it-secret-2" {
		t.Errorf("secret after update: %q, %v", secret, err)
	}

	// 他のクライアントが使っている IP には変えられない。
	other, err := c.CreateRADIUSClient(ctx, RADIUSClientCreate{IP: ip, Secret: "x", Name: "it-ap-02"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup(t, "client "+ip, func(ctx context.Context) error { return c.DeleteRADIUSClient(ctx, other.ID) })
	if other.ID <= created.ID {
		t.Errorf("ids: created %d, other %d", created.ID, other.ID)
	}
	_, err = c.UpdateRADIUSClient(ctx, other.ID, RADIUSClientUpdate{IP: new(newIP)})
	if CauseOf(err) != CauseClientExists {
		t.Errorf("update to used ip: err = %v", err)
	}

	// 削除。
	ctx, tid = opCtx(t)
	if err := c.DeleteRADIUSClient(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	checkAudit(t, tid, "client deleted")
	_, err = c.GetRADIUSClient(t.Context(), created.ID)
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 404 || apiErr.Problem.Cause != CauseClientNotFound {
		t.Errorf("get after delete: err = %v", err)
	}
}

func TestIntegrationPolicies(t *testing.T) {
	c := newIntegrationClient(t)
	// 加入者のない IMSI にも作れる（接続方式01の加入者は policy だけを持つ）。
	imsi := testIMSI()
	cleanup(t, "policy "+imsi, func(ctx context.Context) error { return c.DeletePolicy(ctx, imsi) })

	put := PolicyPut{Default: PolicyDeny, Rules: []PolicyRule{
		{NASID: "it-ap-01", AllowedSSIDs: []string{"CORP-WIFI", "LAB"}, VLANID: "100", SessionTimeout: 3600},
		{NASID: "*", AllowedSSIDs: []string{"*"}},
	}}
	ctx, tid := opCtx(t)
	p, created, err := c.PutPolicy(ctx, imsi, put)
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	want := Policy{IMSI: imsi, Default: put.Default, Rules: put.Rules}
	if !policyEqual(p, want) {
		t.Errorf("created = %+v", p)
	}
	if e := checkAudit(t, tid, "policy created"); e.Msg != "" && (e.TargetIMSI != imsi || e.Details != "default=deny, rules=2") {
		t.Errorf("audit create = %+v", e)
	}
	if got, err := c.GetPolicy(t.Context(), imsi); err != nil || !policyEqual(got, want) {
		t.Errorf("get: %+v, %v", got, err)
	}
	list, err := c.ListPolicies(t.Context(), ListParams{Prefix: imsi})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || !policyEqual(list.Items[0], want) {
		t.Errorf("list: %+v, %v", list, err)
	}

	// 置き換え（ルールを空にする）。
	ctx, tid = opCtx(t)
	p, created, err = c.PutPolicy(ctx, imsi, PolicyPut{Default: PolicyAllow})
	if err != nil || created || p.Default != PolicyAllow || p.Rules == nil || len(p.Rules) != 0 {
		t.Errorf("replace: %+v, %v, %v", p, created, err)
	}
	if e := checkAudit(t, tid, "policy updated"); e.Msg != "" && e.Details != "default: deny -> allow, rules: changed (2 -> 0)" {
		t.Errorf("audit replace = %+v", e)
	}

	// ルールの項目の誤りは、ルールと SSID の位置つきで返る。
	ctx, _ = opCtx(t)
	_, _, err = c.PutPolicy(ctx, imsi, PolicyPut{Default: PolicyDeny, Rules: []PolicyRule{
		{NASID: "*", AllowedSSIDs: []string{"OK", strings.Repeat("x", 33)}},
	}})
	apiErr, ok := errors.AsType[*Error](err)
	if !ok || apiErr.Status != 400 ||
		!slices.ContainsFunc(apiErr.Problem.InvalidParams, func(p InvalidParam) bool { return p.Param == "rules[0].allowedSsids[1]" }) {
		t.Errorf("invalid ssid: err = %#v", err)
	}

	// 削除。
	ctx, tid = opCtx(t)
	if err := c.DeletePolicy(ctx, imsi); err != nil {
		t.Fatal(err)
	}
	checkAudit(t, tid, "policy deleted")
	_, err = c.GetPolicy(t.Context(), imsi)
	if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.Status != 404 || apiErr.Problem.Cause != CausePolicyNotFound {
		t.Errorf("get after delete: err = %v", err)
	}
}

func policyEqual(a, b Policy) bool {
	return a.IMSI == b.IMSI && a.Default == b.Default &&
		slices.EqualFunc(a.Rules, b.Rules, func(x, y PolicyRule) bool {
			return x.NASID == y.NASID && x.VLANID == y.VLANID && x.SessionTimeout == y.SessionTimeout &&
				slices.Equal(x.AllowedSSIDs, y.AllowedSSIDs)
		})
}

// 登録していないクライアント証明書は TLS ハンドシェイクで拒否され、Diagnose がその旨を示す。
func TestIntegrationUnregisteredClient(t *testing.T) {
	registered := newIntegrationClient(t)
	dir := t.TempDir()
	certPEM, keyPEM, err := certs.SelfSignedClient("it-unregistered", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "client.pem"), append(certPEM, keyPEM...))
	c, err := New(Options{
		BaseURL:        registered.BaseURL(),
		ClientCertFile: filepath.Join(dir, "client.pem"),
		ServerCertFile: os.Getenv("EAPAKA_WEBGUI_TEST_ADMIN_SERVER_CERT"),
		Log:            discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Status(t.Context())
	if !IsUnavailable(err) || !strings.Contains(Diagnose(err), "PROVISIONING_API_ADMIN_CLIENTS") {
		t.Errorf("err = %v, diagnose = %q", err, Diagnose(err))
	}
	// provisioning-api は拒否した証明書のフィンガープリントをログに残す。
	fp := certs.Fingerprint(c.ClientCertificate())
	for range 20 {
		if logContains(t, fp) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("PROV_CLIENT_REJECTED with fingerprint %s not found", fp)
}
