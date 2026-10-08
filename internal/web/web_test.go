package web

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/auth/authtest"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

const (
	ownerID = "root"
	ownerPW = "owner-password-123"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// testEnv は画面のテスト環境。認証は本物の auth.Service をメモリ上のストアで動かす。
type testEnv struct {
	h     http.Handler
	auth  *auth.Service
	store *authtest.MemStore
	prov  *fakeProv
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return newTestEnv(t, newFakeProv()).h
}

func newTestEnv(t *testing.T, prov *fakeProv) *testEnv {
	t.Helper()
	return newTestEnvLog(t, prov, discard)
}

func newTestEnvLog(t *testing.T, prov *fakeProv, log *slog.Logger) *testEnv {
	t.Helper()
	st := authtest.NewMemStore()
	svc, err := auth.New(t.Context(), auth.Options{
		Store: st, Log: discard, InitialAdminID: ownerID, InitialAdminPassword: ownerPW,
		SessionIdleTimeout: 30 * time.Minute, SessionMaxAge: 12 * time.Hour,
		MaxLoginFailures: 5, LockDuration: 15 * time.Minute, AuditMaxLen: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Log: log, Version: "test", Prov: prov, Auth: svc})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{h: h.Routes(), auth: svc, store: st, prov: prov}
}

// request はリクエストを作る。cookie があれば付ける。form があれば POST のフォームとして送る。
func request(method, path string, cookie *http.Cookie, form url.Values) *http.Request {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://gui.example"+path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// ブラウザの同一オリジンのリクエストと同じヘッダーを付ける。
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// sessionCookieOf は応答で設定されたセッションの Cookie を返す。
func sessionCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

// loginAs はログインしてセッションの Cookie を返す。
func (e *testEnv) loginAs(t *testing.T, id, pw string) *http.Cookie {
	t.Helper()
	w := do(e.h, request("POST", "/login", nil, url.Values{"id": {id}, "password": {pw}, "next": {"/"}}))
	c := sessionCookieOf(w)
	if w.Code != http.StatusSeeOther || c == nil || c.Value == "" {
		t.Fatalf("login %s: status %d, cookie %v", id, w.Code, c)
	}
	return c
}

func TestDashboard(t *testing.T) {
	prov := newFakeProv()
	prov.status = provapi.Status{Version: "0.2.0", NodeName: "poc-01", StartedAt: time.Now(),
		SubscriberCount: 12, ClientCount: 3, PolicyCount: 7}
	env := newTestEnv(t, prov)
	w := do(env.h, request("GET", "/", env.loginAs(t, ownerID, ownerPW), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<title>ダッシュボード | EAP-AKA RADIUS 管理</title>", "/static/htmx.min.js",
		"web-gui-for-eapaka-radius test", `<p class="metric">12</p>`, `<p class="metric">3</p>`, `<p class="metric">7</p>`,
		"<td>poc-01</td>", "<td>0.2.0</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestDashboardProvError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"unreachable": {
			err:  fmt.Errorf("wrap: %w", &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}),
			want: "<p>provisioning-api に接続できません。本PoCの provisioning-api が起動しているか",
		},
		"unknown": {
			err:  fmt.Errorf("something odd"),
			want: "<p>本PoCの Provisioning API に接続できません。</p>",
		},
		"api error": {
			err:  &provapi.Error{Status: 500, Problem: provapi.Problem{Cause: provapi.CauseSystemFailure}},
			want: "Provisioning API がエラーを返しました。",
		},
	} {
		prov := newFakeProv()
		prov.err = tc.err
		env := newTestEnv(t, prov)
		w := do(env.h, request("GET", "/", env.loginAs(t, ownerID, ownerPW), nil))
		// Provisioning API に届かなくても画面自体は返す。
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d", name, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, tc.want) || strings.Contains(body, `class="metric"`) {
			t.Errorf("%s: body = %s", name, body)
		}
	}
}

func TestNotFound(t *testing.T) {
	h := newTestHandler(t)
	for _, path := range []string{"/nope", "/static/", "/static/nope.js"} {
		w := do(h, httptest.NewRequest("GET", "https://gui.example"+path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "ページが見つかりません。") {
			t.Errorf("%s: 404 page body: %s", path, w.Body.String())
		}
	}
}

func TestStatic(t *testing.T) {
	h := newTestHandler(t)
	for path, ctype := range map[string]string{
		"/static/htmx.min.js":  "text/javascript; charset=utf-8",
		"/static/pico.min.css": "text/css; charset=utf-8",
		"/static/app.css":      "text/css; charset=utf-8",
	} {
		w := do(h, httptest.NewRequest("GET", "https://gui.example"+path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d", path, w.Code)
			continue
		}
		if got := w.Header().Get("Content-Type"); got != ctype {
			t.Errorf("%s: Content-Type = %q", path, got)
		}
		etag := w.Header().Get("ETag")
		if etag == "" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: ETag = %q, Cache-Control = %q", path, etag, w.Header().Get("Cache-Control"))
			continue
		}

		// 同じ ETag なら 304 を返す。
		r := httptest.NewRequest("GET", "https://gui.example"+path, nil)
		r.Header.Set("If-None-Match", etag)
		if w := do(h, r); w.Code != http.StatusNotModified {
			t.Errorf("%s: conditional status = %d", path, w.Code)
		}
	}
}

func TestCrossOriginProtection(t *testing.T) {
	h := newTestHandler(t)

	r := httptest.NewRequest("POST", "https://gui.example/login", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := do(h, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "別のサイトからの操作は受け付けません。") {
		t.Errorf("cross-site POST body: %s", w.Body.String())
	}

	// 同一オリジンの POST は通す（ここではルートがないので 404 になる）。
	r = httptest.NewRequest("POST", "https://gui.example/", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if w := do(h, r); w.Code != http.StatusNotFound {
		t.Errorf("same-origin POST: status = %d", w.Code)
	}
}

func TestAccessLogTraceID(t *testing.T) {
	var buf bytes.Buffer
	env := newTestEnvLog(t, newFakeProv(), slog.New(slog.NewJSONHandler(&buf, nil)))
	do(env.h, request("GET", "/login?next=/subscribers?prefix=001010000000001", nil, nil))

	var entry struct {
		Msg     string `json:"msg"`
		Path    string `json:"path"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if entry.Msg != "access" || entry.Path != "/login" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(entry.TraceID) {
		t.Errorf("access log = %s", buf.String())
	}
	// クエリ文字列は出さない。
	if strings.Contains(buf.String(), "001010000000001") {
		t.Errorf("query string is logged: %s", buf.String())
	}
}

func TestDatetime(t *testing.T) {
	f := funcs["datetime"].(func(time.Time) string)
	if got := f(time.Time{}); got != "-" {
		t.Errorf("zero = %q", got)
	}
	if got := f(time.Date(2026, 10, 3, 15, 4, 5, 0, time.UTC)); !strings.HasPrefix(got, "2026-10-0") {
		t.Errorf("got %q", got)
	}
}
