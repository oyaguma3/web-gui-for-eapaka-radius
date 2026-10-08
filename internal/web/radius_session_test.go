package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

func TestSessions(t *testing.T) {
	e := newScreenEnv(t)
	start := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	e.prov.sessions = []provapi.Session{
		{ID: "u1", IMSI: "001010000000001", NasIP: "192.0.2.1", NasIdentifier: "AP-01", StartTime: start,
			ClientIP: "10.0.0.5", AcctSessionID: "A1", InputOctets: 1536, OutputOctets: 200},
		// Accounting-Request を受ける前のセッション。
		{ID: "u2", IMSI: "001010000000002", NasIP: "192.0.2.2"},
	}

	// 一般ユーザーも見られる。
	w := do(e.h, request("GET", "/sessions", e.user, nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || e.prov.lastSessionParams != (provapi.SessionParams{Limit: sessionsLimit}) {
		t.Fatalf("%d %+v", w.Code, e.prov.lastSessionParams)
	}
	for _, want := range []string{
		"アクティブセッション 全 2 件", `<a href="/subscribers/001010000000001">001010000000001</a>`, "AP-01<br><small class=\"muted\">192.0.2.1</small>",
		"<td>10.0.0.5</td>", `<span title="1536 octets">1.5 KiB</span> / <span title="200 octets">200 B</span>`, "<code class=\"wrap\">A1</code>",
		"<code class=\"wrap\">u1</code>", `<td><small class="muted">192.0.2.2</small></td>`, "<td>-</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions page does not contain %q", want)
		}
	}
	if strings.Contains(body, "だけを表示しています") {
		t.Error("not truncated")
	}

	// IMSI で絞り込む。
	body = do(e.h, request("GET", "/sessions?imsi=001010000000002", e.user, nil)).Body.String()
	if e.prov.lastSessionParams.IMSI != "001010000000002" || !strings.Contains(body, "IMSI 001010000000002 のアクティブセッション 1 件") ||
		strings.Contains(body, "001010000000001") || !strings.Contains(body, `<a href="/sessions">すべてのセッションを表示</a>`) {
		t.Errorf("filtered: %s", body)
	}
	body = do(e.h, request("GET", "/sessions?imsi=001010000000009", e.user, nil)).Body.String()
	if !strings.Contains(body, "この加入者のアクティブセッションはありません。") {
		t.Error("no sessions for imsi")
	}

	// 形式の正しくない IMSI は Provisioning API に送らない。
	e.prov.lastSessionParams = provapi.SessionParams{}
	if w := do(e.h, request("GET", "/sessions?imsi=12345", e.user, nil)); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "IMSI は 15 桁の数字で入力してください。") || e.prov.lastSessionParams != (provapi.SessionParams{}) {
		t.Errorf("bad imsi: %d", w.Code)
	}

	// 上限を超えると、一部だけを出していることを示す。
	e.prov.sessions = nil
	for i := range sessionsLimit + 5 {
		e.prov.sessions = append(e.prov.sessions, provapi.Session{ID: fmt.Sprintf("s%03d", i), IMSI: "001010000000001"})
	}
	body = do(e.h, request("GET", "/sessions", e.user, nil)).Body.String()
	if !strings.Contains(body, "全 105 件") || !strings.Contains(body, "接続開始の新しい 100 件だけを表示しています。") {
		t.Error("truncated")
	}

	e.prov.sessions = nil
	if body := do(e.h, request("GET", "/sessions", e.user, nil)).Body.String(); !strings.Contains(body, "<p>アクティブセッションはありません。</p>") {
		t.Error("empty")
	}
}

func TestSubscriberSessionsLink(t *testing.T) {
	e := newScreenEnv(t)
	e.seed(t, "001010000000001")
	if body := do(e.h, request("GET", "/subscribers/001010000000001", e.user, nil)).Body.String(); !strings.Contains(body, `<a href="/sessions?imsi=001010000000001">`) {
		t.Error("subscriber page has no link to sessions")
	}
}

func TestFormatOctets(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 20 << 20: "20.0 MiB", 3 << 30: "3.0 GiB",
		5 << 40: "5.0 TiB", 2048 << 50: "2048.0 PiB",
	} {
		if got := formatOctets(n); got != want {
			t.Errorf("formatOctets(%d) = %q, want %q", n, got, want)
		}
	}
}
