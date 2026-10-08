package web

import (
	"net/http"
	"strings"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// sessionsLimit はアクティブセッションの一覧に出す件数の上限（新しい順）。
const sessionsLimit = 100

// sessionListData はアクティブセッションの一覧に渡す値。
type sessionListData struct {
	IMSI  string
	Items []provapi.Session
	// Total は条件に一致するセッションの総数（Items は sessionsLimit 件まで）。
	Total int64
	Error string
}

// Truncated は、一致したセッションの一部だけを出しているか。
func (d sessionListData) Truncated() bool { return d.Total > int64(len(d.Items)) }

// sessions はアクティブセッション（RADIUS の認証で作られ、Accounting-Stop で消える）の一覧。全員が使える。
// 読み出しだけで、provisioning-api の監査ログには残らない。
func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	d := sessionListData{IMSI: strings.TrimSpace(r.URL.Query().Get("imsi"))}
	if d.IMSI != "" && !imsiPattern.MatchString(d.IMSI) {
		d.Error = "IMSI は 15 桁の数字で入力してください。"
		h.render(w, r, http.StatusBadRequest, "sessions", "セッション", d)
		return
	}
	l, err := h.prov.ListSessions(r.Context(), provapi.SessionParams{IMSI: d.IMSI, Limit: sessionsLimit})
	if err != nil {
		status, msg := monitoringErrorMessage(err, "セッションの参照")
		h.log.Warn("list sessions", "error", err)
		d.Error = msg
		h.render(w, r, status, "sessions", "セッション", d)
		return
	}
	d.Items, d.Total = l.Items, l.Total
	h.render(w, r, http.StatusOK, "sessions", "セッション", d)
}
