package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

func (e *screenEnv) seedClient(t *testing.T, ip, name string) int64 {
	t.Helper()
	c, err := e.prov.CreateRADIUSClient(t.Context(), provapi.RADIUSClientCreate{IP: ip, Secret: "s3cret!", Name: name, Vendor: "generic"})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestClientListAndPermissions(t *testing.T) {
	e := newScreenEnv(t)
	e.seedClient(t, "192.0.2.10", "AP-01")

	// 一般ユーザーは一覧と詳細を見られるが、登録・変更・削除・共有シークレットの表示はできない。
	body := do(e.h, request("GET", "/clients", e.user, nil)).Body.String()
	if !strings.Contains(body, `<a href="/clients/1">192.0.2.10</a>`) || strings.Contains(body, "RADIUSクライアントの登録") {
		t.Errorf("user list: %s", body)
	}
	body = do(e.h, request("GET", "/clients/1", e.user, nil)).Body.String()
	if !strings.Contains(body, "AP-01") || strings.Contains(body, "共有シークレットを表示") || strings.Contains(body, "このクライアントを削除") {
		t.Errorf("user detail: %s", body)
	}
	for _, path := range []string{"/clients", "/clients/1/update", "/clients/1/secret", "/clients/1/delete"} {
		if w := do(e.h, request("POST", path, e.user, url.Values{})); w.Code != http.StatusForbidden {
			t.Errorf("user POST %s: %d", path, w.Code)
		}
	}
	if strings.Contains(strings.Join(e.prov.calls, ","), "bob") {
		t.Errorf("calls by user: %v", e.prov.calls)
	}

	body = do(e.h, request("GET", "/clients", e.owner, nil)).Body.String()
	if !strings.Contains(body, "RADIUSクライアントの登録") {
		t.Errorf("owner list: %s", body)
	}
	if w := do(e.h, request("GET", "/clients/9", e.owner, nil)); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "他の操作") {
		t.Errorf("not found: %d", w.Code)
	}
	if w := do(e.h, request("GET", "/clients/abc", e.owner, nil)); w.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", w.Code)
	}
}

func TestClientCreate(t *testing.T) {
	e := newScreenEnv(t)
	form := url.Values{"ip": {"192.0.2.10"}, "secret": {"s3cret!"}, "name": {"AP-01"}, "vendor": {""}}
	w := do(e.h, hx(request("POST", "/clients", e.owner, form)))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "RADIUSクライアント AP-01（#1、192.0.2.10）を登録しました。") ||
		!strings.HasPrefix(strings.TrimSpace(body), `<div id="clients">`) {
		t.Errorf("create: %d %s", w.Code, body)
	}
	if a := e.lastAudit(); a.Action != auditClientCreate || a.Target != "#1" || strings.Contains(a.Detail, "s3cret") ||
		!strings.Contains(a.Detail, `"ip":"192.0.2.10"`) {
		t.Errorf("audit = %+v", a)
	}

	w = do(e.h, hx(request("POST", "/clients", e.owner, form)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "その IP アドレスの RADIUSクライアントは既に登録されています（本PoCの Admin TUI など、他の操作で登録された場合もあります）。") {
		t.Errorf("duplicate: %d", w.Code)
	}

	bad := url.Values{"ip": {"192.168.010.1"}, "secret": {"has space"}, "name": {"AP 01"}, "vendor": {"x_y"}}
	w = do(e.h, hx(request("POST", "/clients", e.owner, bad)))
	body = w.Body.String()
	for _, want := range []string{msgClientIP, msgClientSecret, msgClientName, msgClientVendor, `value="192.168.010.1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("invalid: missing %q", want)
		}
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid: %d", w.Code)
	}
}

func TestClientUpdateSecretDelete(t *testing.T) {
	e := newScreenEnv(t)
	id := e.seedClient(t, "192.0.2.10", "AP-01")
	e.seedClient(t, "192.0.2.20", "AP-02")
	path := "/clients/1/update"

	// IP を変えても ID は変わらない。変わった項目だけを送り、共有シークレットは空なら送らない。
	w := do(e.h, hx(request("POST", path, e.owner, url.Values{"ip": {"192.0.2.11"}, "secret": {""}, "name": {"AP-01"}, "vendor": {""}})))
	u := e.prov.lastClientUpdate
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "IP アドレス、ベンダー を変更しました。") ||
		u.IP == nil || *u.IP != "192.0.2.11" || u.Vendor == nil || *u.Vendor != "" || u.Name != nil || u.Secret != nil {
		t.Errorf("update: %d %+v %s", w.Code, u, w.Body.String())
	}
	if c := e.prov.clients[id]; c.IP != "192.0.2.11" {
		t.Errorf("client = %+v", c)
	}
	if a := e.lastAudit(); a.Action != auditClientUpdate || !strings.Contains(a.Detail, `"ip":"192.0.2.10 -> 192.0.2.11"`) {
		t.Errorf("audit = %+v", a)
	}

	w = do(e.h, hx(request("POST", path, e.owner, url.Values{"ip": {"192.0.2.11"}, "secret": {"new-secret"}, "name": {"AP-01"}, "vendor": {""}})))
	if !strings.Contains(w.Body.String(), "共有シークレット を変更しました。") || e.prov.secrets[id] != "new-secret" ||
		strings.Contains(e.lastAudit().Detail, "new-secret") {
		t.Errorf("secret: %s", w.Body.String())
	}

	// 他のクライアントの IP には変えられない。
	w = do(e.h, hx(request("POST", path, e.owner, url.Values{"ip": {"192.0.2.20"}, "name": {"AP-01"}})))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "既に登録されています") || !strings.Contains(w.Body.String(), `value="192.0.2.20"`) {
		t.Errorf("conflict: %d %s", w.Code, w.Body.String())
	}

	// 共有シークレットは押したときだけ取得し、監査ログに残す。
	w = do(e.h, hx(request("POST", "/clients/1/secret", e.owner, url.Values{})))
	if !strings.Contains(w.Body.String(), "<code>new-secret</code>") || e.prov.lastCall() != "GetRADIUSClientSecret root" {
		t.Errorf("secret: %s", w.Body.String())
	}
	if a := e.lastAudit(); a.Action != auditClientSecretRead || a.Target != "#1" || strings.Contains(a.Detail, "new-secret") {
		t.Errorf("audit = %+v", a)
	}

	w = do(e.h, hx(request("POST", "/clients/1/delete", e.owner, url.Values{})))
	if w.Header().Get("HX-Redirect") != "/clients?deleted=1" || e.lastAudit().Action != auditClientDelete {
		t.Errorf("delete: %v", w.Header())
	}
	body := do(e.h, request("GET", "/clients?deleted=1", e.owner, nil)).Body.String()
	if !strings.Contains(body, "RADIUSクライアント #1 を削除しました。") {
		t.Errorf("deleted: %s", body)
	}
	w = do(e.h, hx(request("POST", "/clients/1/delete", e.owner, url.Values{})))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "他の操作") {
		t.Errorf("already deleted: %d", w.Code)
	}
}
