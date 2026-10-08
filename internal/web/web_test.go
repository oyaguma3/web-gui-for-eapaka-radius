package web

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes()
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHome(t *testing.T) {
	h := newTestHandler(t)
	w := do(h, httptest.NewRequest("GET", "https://gui.example/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"<title>ホーム | EAP-AKA RADIUS 管理</title>", "/static/htmx.min.js", "web-gui-for-eapaka-radius test"} {
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

	r := httptest.NewRequest("POST", "https://gui.example/", nil)
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
	h, err := New(Options{Log: slog.New(slog.NewJSONHandler(&buf, nil)), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	do(h.Routes(), httptest.NewRequest("GET", "https://gui.example/?imsi=001010000000001", nil))

	var entry struct {
		Msg     string `json:"msg"`
		Path    string `json:"path"`
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if entry.Msg != "access" || entry.Path != "/" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(entry.TraceID) {
		t.Errorf("access log = %s", buf.String())
	}
	// クエリ文字列は出さない。
	if strings.Contains(buf.String(), "001010000000001") {
		t.Errorf("query string is logged: %s", buf.String())
	}
}
