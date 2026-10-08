package web

import (
	"cmp"
	"maps"
	"net/http"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/trace"
)

// auditPerPage は監査ログの 1 回に読み込む件数。
const auditPerPage = 50

// BFF を通して行った Provisioning API の操作の、BFF の監査ログでの名前。
const (
	auditSubscriberCreate   = "subscriber.create"
	auditSubscriberUpdate   = "subscriber.update"
	auditSubscriberDelete   = "subscriber.delete"
	auditSubscriberKeysRead = "subscriber.keys.read"
	auditClientCreate       = "client.create"
	auditClientUpdate       = "client.update"
	auditClientDelete       = "client.delete"
	auditClientSecretRead   = "client.secret.read"
	auditPolicyPut          = "policy.put"
	auditPolicyDelete       = "policy.delete"
)

// auditActionLabels は監査ログの操作の名前。
var auditActionLabels = map[string]string{
	auditSubscriberCreate:   "加入者の登録",
	auditSubscriberUpdate:   "加入者の変更",
	auditSubscriberDelete:   "加入者の削除",
	auditSubscriberKeysRead: "Ki / OPc の表示",
	auditClientCreate:       "RADIUSクライアントの登録",
	auditClientUpdate:       "RADIUSクライアントの変更",
	auditClientDelete:       "RADIUSクライアントの削除",
	auditClientSecretRead:   "共有シークレットの表示",
	auditPolicyPut:          "認可ポリシーの保存",
	auditPolicyDelete:       "認可ポリシーの削除",
	// provisioning-api の監査ログでは、認可ポリシーの保存を作成と変更に分けて記録する。
	"policy.create":           "認可ポリシーの作成",
	"policy.update":           "認可ポリシーの変更",
	"login.success":           "ログイン",
	"login.failure":           "ログインの失敗",
	"account.create":          "アカウントの作成",
	"account.delete":          "アカウントの削除",
	"account.password.reset":  "パスワードの再設定",
	"account.password.change": "パスワードの変更",
}

// record は、Provisioning API の操作が成功したことを BFF の監査ログに残す。
// トレースID を加えるので、provisioning-api の監査ログ（trace_id）と突き合わせられる。
// detail に秘密の値（Ki / OPc、共有シークレット）を入れてはならない。
func (h *Handler) record(r *http.Request, action, target string, detail map[string]any) {
	me, _ := accountFrom(r.Context())
	d := map[string]any{"trace_id": trace.From(r.Context())}
	maps.Copy(d, detail)
	h.auth.Record(r.Context(), me, action, target, d)
}

// auditRow は監査ログの 1 行。
type auditRow struct {
	Time     time.Time
	Operator string
	Remote   string
	Action   string
	Target   string
	Detail   string
}

type auditData struct {
	Rows  []auditRow
	Next  string
	Error string
	// More は「さらに古いものを表示」で続きを読み込んだ応答か。
	More bool
}

// audit は BFF の監査ログの画面。管理者だけが使える。
// provisioning-api の監査ログは、同じ画面の別のタブ（provAudit）で見る。
func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := auditData{More: q.Get("before") != ""}
	status := http.StatusOK
	me, _ := accountFrom(r.Context())
	entries, next, err := h.auth.ListAudit(r.Context(), me, q.Get("before"), auditPerPage)
	if err != nil {
		status, d.Error = http.StatusInternalServerError, "BFF の監査ログを取得できませんでした。"
		h.log.Error("list bff audit", "error", err)
	}
	for _, e := range entries {
		d.Rows = append(d.Rows, auditRow{
			Time: e.Time, Operator: e.Actor, Remote: e.Remote, Action: e.Action, Target: e.Target, Detail: e.Detail,
		})
	}
	d.Next = next
	if d.More && r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "audit", "audit-more", d)
		return
	}
	h.render(w, r, status, "audit", "監査ログ", d)
}

// provAuditData は provisioning-api の監査ログの画面に渡す値。
type provAuditData struct {
	Rows  []provapi.AuditLogEntry
	Next  string
	Error string
	// More は「さらに古いものを表示」で続きを読み込んだ応答か。
	More bool
}

// provAudit は provisioning-api の監査ログ（GET /audit-logs）の画面。管理者だけが使える。
// 本PoCの Admin TUI での操作は含まない（provisioning-api を通した操作だけ）。
func (h *Handler) provAudit(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	d := provAuditData{More: before != ""}
	status := http.StatusOK
	if before != "" && !streamIDPattern.MatchString(before) {
		status, d.Error = http.StatusBadRequest, "続きの位置の指定が正しくありません。"
	} else if l, err := h.prov.ListAuditLogs(r.Context(), provapi.AuditLogParams{Before: before, Limit: auditPerPage}); err != nil {
		status, d.Error = monitoringErrorMessage(err, "監査ログの参照")
		h.log.Warn("list provisioning api audit logs", "error", err)
	} else {
		d.Rows, d.Next = l.Items, l.NextBefore
	}
	if d.More && r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "audit_prov", "audit-prov-more", d)
		return
	}
	h.render(w, r, status, "audit_prov", "監査ログ（provisioning-api）", d)
}

// actionLabel は監査ログの操作の名前を返す。
func actionLabel(action string) string { return cmp.Or(auditActionLabels[action], action) }
