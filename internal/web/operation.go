package web

import (
	"net/http"
	"regexp"
	"slices"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// eapaka-node-provisioner の操作の記録の画面（設計概要 §12）。2 つのノードにまたがる加入者の登録・変更・削除の、
// 手順ごとの状態を見る。閲覧は全員、やり直し（retry）と閉じる（dismiss）は管理者だけ
// （下流の状態を確かめたうえで判断するため）。

// operationsPerPage は操作の記録の一覧の件数の上限。
const operationsPerPage = 100

// operationIDPattern は操作の ID（UUID）の形式。
var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// operationStatuses は一覧で絞り込める状態（未完了のもの）。
var operationStatuses = []string{pvapi.OpRunning, pvapi.OpRetrying, pvapi.OpFailed}

// 操作の記録への操作の、BFF の監査ログでの名前。
const (
	auditOperationRetry   = "operation.retry"
	auditOperationDismiss = "operation.dismiss"
)

type operationListData struct {
	// Status は絞り込みの状態（空ならすべて）。
	Status   string
	Statuses []string
	Items    []pvapi.Operation
	Total    int
	Error    string
}

func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	d := operationListData{Status: r.URL.Query().Get("status"), Statuses: operationStatuses}
	if d.Status != "" && !slices.Contains(operationStatuses, d.Status) {
		d.Error = "状態の指定が正しくありません。"
		h.render(w, r, http.StatusBadRequest, "operations", "操作の記録", d)
		return
	}
	list, err := h.pv.ListOperations(r.Context(), d.Status, operationsPerPage)
	if err != nil {
		status, msg := h.apiErrorMessage(err, "")
		h.log.Warn("list operations", "error", err)
		d.Error = msg
		h.render(w, r, status, "operations", "操作の記録", d)
		return
	}
	d.Items, d.Total = list.Items, list.Total
	h.render(w, r, http.StatusOK, "operations", "操作の記録", d)
}

type operationData struct {
	Op pvapi.Operation
	// CanAct は、やり直し・閉じるができるか（管理者のみ）。
	CanAct bool
	// RetryKey と DismissKey は、やり直しと閉じるのフォームの Idempotency-Key。
	RetryKey, DismissKey string
	Message              string
	Error                string
}

// CanRetry は、この操作をやり直せるか（failed だけ。鍵の変更が反映されたか分からない変更は除く）。
func (d operationData) CanRetry() bool {
	return d.Op.Status == pvapi.OpFailed && !d.KeyChangeUnknown()
}

// CanDismiss は、この操作を閉じられるか（retrying / failed）。
func (d operationData) CanDismiss() bool {
	return d.Op.Status == pvapi.OpRetrying || d.Op.Status == pvapi.OpFailed
}

// KeyChangeUnknown は、鍵の変更が反映されたかどうか分からない変更か（provisioner は自動でも手でもやり直さない）。
// 変更の記録で、鍵の変更の手順が済んでも失敗してもいない（未実施のまま要求が落ちた）まま failed になったもの。
func (d operationData) KeyChangeUnknown() bool {
	if d.Op.Kind != "subscriber.update" || d.Op.Status != pvapi.OpFailed {
		return false
	}
	i := slices.IndexFunc(d.Op.Steps, func(s pvapi.OperationStep) bool { return s.Name == "subscriber.update" })
	return i >= 0 && d.Op.Steps[i].State == "pending"
}

// operationID は URL の操作の ID を返す。形式が違えば 404 を返して false。
func (h *Handler) operationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !operationIDPattern.MatchString(id) {
		h.notFound(w, r)
		return "", false
	}
	return id, true
}

// operationNotFound は操作の記録が見つからないときの説明。
const operationNotFound = "操作の記録が見つかりません。完了した操作の記録は 7 日で消えます。"

// renderOperation は操作の記録の詳細を返す（htmx では詳細の部分だけ）。
func (h *Handler) renderOperation(w http.ResponseWriter, r *http.Request, id string, status int, mutate func(*operationData)) {
	op, err := h.pv.GetOperation(r.Context(), id)
	if err != nil {
		f := h.apiError(err, operationNotFound)
		if f.Status != http.StatusNotFound {
			h.log.Warn("get operation", "error", err)
		}
		h.renderFailure(w, r, f, "/operations", "操作の記録の一覧へ")
		return
	}
	me, _ := accountFrom(r.Context())
	d := operationData{Op: op, CanAct: me.IsAdmin(), RetryKey: newIdemKey(), DismissKey: newIdemKey()}
	if mutate != nil {
		mutate(&d)
	}
	if r.Header.Get("HX-Request") == "true" {
		h.renderBlock(w, r, status, "operation", "operation-detail", d)
		return
	}
	h.render(w, r, status, "operation", "操作の記録", d)
}

func (h *Handler) operation(w http.ResponseWriter, r *http.Request) {
	if id, ok := h.operationID(w, r); ok {
		h.renderOperation(w, r, id, http.StatusOK, nil)
	}
}

// operationRetry は failed の操作の続きを、その場で 1 回行う。管理者だけが使える。
func (h *Handler) operationRetry(w http.ResponseWriter, r *http.Request) {
	h.operationAction(w, r, auditOperationRetry)
}

// operationDismiss は retrying / failed の操作を、手で直した後に閉じる（下流には何もしない）。管理者だけが使える。
func (h *Handler) operationDismiss(w http.ResponseWriter, r *http.Request) {
	h.operationAction(w, r, auditOperationDismiss)
}

func (h *Handler) operationAction(w http.ResponseWriter, r *http.Request, action string) {
	id, ok := h.operationID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "入力を読み取れませんでした。")
		return
	}
	idem := formIdemKey(r)
	var op pvapi.Operation
	var err error
	if action == auditOperationRetry {
		op, err = h.pv.RetryOperation(r.Context(), id, idem)
	} else {
		op, err = h.pv.DismissOperation(r.Context(), id, idem)
	}
	if err != nil {
		f := h.apiError(err, operationNotFound)
		h.log.Warn("operation "+action, "operation_id", id, "error", err)
		h.renderOperation(w, r, id, f.Status, func(d *operationData) {
			d.Error = f.Message
			if action == auditOperationRetry {
				d.RetryKey = keepIdemKey(err, idem)
			} else {
				d.DismissKey = keepIdemKey(err, idem)
			}
		})
		return
	}
	h.record(r, action, id, map[string]any{"imsi": op.IMSI, "kind": op.Kind, "status": op.Status})
	h.renderOperation(w, r, id, http.StatusOK, func(d *operationData) {
		switch {
		case action == auditOperationDismiss:
			d.Message = "操作を閉じました。"
		case op.Status == pvapi.OpRetrying:
			d.Error = "やり直しましたが、また失敗しました。provisioner が自動でのやり直しを再開します（24 時間）。"
		default:
			d.Message = "やり直しました。操作は「" + opStatusLabel(op.Status) + "」になりました。"
		}
	})
}
