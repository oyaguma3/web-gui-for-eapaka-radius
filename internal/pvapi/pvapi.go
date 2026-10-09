// Package pvapi は eapaka-node-provisioner（以下「provisioner」）の API のクライアント。
// API 仕様は eapaka-node-provisioner のリポジトリの docs/openapi/provisioner-api.yaml（0.2.0）を参照。
//
// 通信（mTLS、操作者の X-Operator-Id、トレースID、エラー）は、接続先を provisioner にした provapi.Client を使う。
// provisioner は RADIUSクライアント・認可ポリシー・セッション・鍵の取得を Provisioning API と同じ形で中継するので、
// それらは provapi.Client のメソッドをそのまま使う。このパッケージは provisioner だけの部分
// （状態、加入者の統合操作、操作の記録、監査ログ）を扱う。
package pvapi

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// Name は provisioner の API の名前（provapi.Options.Name に使う。ログとエラーに出る）。
const Name = "provisioner"

// provisioner の ProblemDetails の cause の値（Provisioning API と共通のものは provapi にある）。
const (
	CauseKeyStoreMismatch        = "KEY_STORE_MISMATCH"
	CauseOperationInProgress     = "OPERATION_IN_PROGRESS"
	CauseOperationUnresolved     = "OPERATION_UNRESOLVED"
	CauseOperationIncomplete     = "OPERATION_INCOMPLETE"
	CauseOperationNotFound       = "OPERATION_NOT_FOUND"
	CauseOperationStateConflict  = "OPERATION_STATE_CONFLICT"
	CauseIdempotencyKeyMismatch  = "IDEMPOTENCY_KEY_MISMATCH"
	CauseDownstreamError         = "DOWNSTREAM_ERROR"
	CauseDownstreamUnavailable   = "DOWNSTREAM_UNAVAILABLE"
	CauseDownstreamNotConfigured = "DOWNSTREAM_NOT_CONFIGURED"
)

// Client は provisioner だけの API のクライアント。
type Client struct {
	c *provapi.Client
}

// New は、接続先を provisioner にした provapi.Client からクライアントを作る。
func New(c *provapi.Client) *Client { return &Client{c: c} }

// ---- 接続先の判別 ----

// API は接続先の種類。
type API string

const (
	APIProvisioningAPI API = "provisioning-api"
	APIProvisioner     API = "provisioner"
)

// Probe は /status の応答の形から、接続先が provisioner か provisioning-api かを見分ける
// （設定 EAPAKA_WEBGUI_ADMIN_API と違う相手につながっていないかを確かめるため）。
// provisioner の /status には downstreams があり、provisioning-api の /status には nodeName がある。
// どちらでもなければ空文字列を返す。
func Probe(ctx context.Context, c *provapi.Client) (API, error) {
	m, err := c.Call[map[string]jsontext.Value](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"status"}})
	if err != nil {
		return "", err
	}
	if _, ok := m["downstreams"]; ok {
		return APIProvisioner, nil
	}
	if _, ok := m["nodeName"]; ok {
		return APIProvisioningAPI, nil
	}
	return "", nil
}

// ---- 状態 ----

// KeyStore は鍵（Ki / OPc）の置き場所。
type KeyStore string

const (
	// KeyStorePoC は本PoCの Vector API（接続方式 00。provisioning-api の加入者）。
	KeyStorePoC KeyStore = "poc"
	// KeyStoreAKA は aka-only-server（接続方式 01。aka-only-server の加入者）。
	KeyStoreAKA KeyStore = "aka"
)

// Status は provisioner の状態。
type Status struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	// PLMNMap は PLMN マップ（本PoCの VECTOR_GATEWAY_PLMN_MAP と同じ値のはず）。
	PLMNMap     []PLMNEntry      `json:"plmnMap"`
	Downstreams Downstreams      `json:"downstreams"`
	AVClient    AVClientStatus   `json:"avClient"`
	Valkey      ValkeyStatus     `json:"valkey"`
	Operations  *OperationCounts `json:"operations"`
}

// PLMNEntry は PLMN マップの 1 件。
type PLMNEntry struct {
	PLMN     string   `json:"plmn"`
	KeyStore KeyStore `json:"keyStore"`
}

// Downstreams は下流 2 つへの接続の状態。
type Downstreams struct {
	Prov DownstreamStatus `json:"prov"`
	Aka  DownstreamStatus `json:"aka"`
}

// DownstreamStatus は下流 1 つへの接続の状態。
type DownstreamStatus struct {
	// Configured は接続先を設定しているか（aka-only-server を使わない設定なら aka は false）。
	Configured      bool   `json:"configured"`
	URL             string `json:"url"`
	Reachable       bool   `json:"reachable"`
	Version         string `json:"version"`
	NodeName        string `json:"nodeName"`
	SubscriberCount int64  `json:"subscriberCount"`
	// Error と Hint は接続できなかった場合のエラーと原因の見当（provisioner が日本語で返す）。
	Error string `json:"error"`
	Hint  string `json:"hint"`
}

// AVClientStatus は vector-gateway の AVクライアント（provisioner の PROVISIONER_AKA_AV_CLIENT_ID）の確認結果。
type AVClientStatus struct {
	// ID は設定の ID（aka-only-server を使わない設定なら 0）。
	ID      int64  `json:"id"`
	Exists  bool   `json:"exists"`
	Enabled bool   `json:"enabled"`
	Name    string `json:"name"`
}

// ValkeyStatus は provisioner 専用の Valkey への接続の状態。
type ValkeyStatus struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error"`
}

// OperationCounts は未解決の操作の件数。
type OperationCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
	Failed   int `json:"failed"`
}

// IsProvisioner は、この状態が provisioner の応答かを返す。provisioner は provisioning-api への接続を必ず設定しているので、
// downstreams.prov.configured が false なら provisioner の応答ではない（provisioning-api につながっている等）。
func (s Status) IsProvisioner() bool { return s.Downstreams.Prov.Configured }

// Status は provisioner の状態を取得する。
func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.c.Call[Status](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"status"}})
}

// ---- 加入者 ----

// Issue は 2 つのノードの状態の食い違い。
type Issue string

const (
	// IssueKeyMissing は、置き場所に加入者（鍵）がないこと。
	IssueKeyMissing Issue = "KEY_MISSING"
	// IssuePolicyMissing は、認可ポリシーがないこと（本PoCは認証を拒否する）。
	IssuePolicyMissing Issue = "POLICY_MISSING"
	// IssueKeyInOtherStore は、置き場所でない方にも同じ IMSI の加入者があること。
	IssueKeyInOtherStore Issue = "KEY_IN_OTHER_STORE"
	// IssueAVClientNotAllowed は、aka-only-server の加入者が vector-gateway の AVクライアントを許可していないこと。
	IssueAVClientNotAllowed Issue = "AV_CLIENT_NOT_ALLOWED"
	// IssueOtherStoreUnreachable は、置き場所でない方の下流に接続できず、KEY_IN_OTHER_STORE を確かめられなかったこと。
	IssueOtherStoreUnreachable Issue = "OTHER_STORE_UNREACHABLE"
)

// Key は置き場所の加入者の属性（Ki と OPc は含まない）。SQNType 以降は aka-only-server の加入者だけ。
type Key struct {
	AMF              string    `json:"amf"`
	SQN              string    `json:"sqn"`
	SQNType          string    `json:"sqnType,omitempty"`
	AllowPlain       *bool     `json:"allowPlain,omitempty"`
	AllowedClientIDs []int64   `json:"allowedClientIds,omitempty"`
	CreatedAt        time.Time `json:"createdAt,omitzero"`
	UpdatedAt        time.Time `json:"updatedAt,omitzero"`
}

// Subscriber は加入者（IMSI＋鍵の置き場所＋認可ポリシー）。
type Subscriber struct {
	IMSI     string   `json:"imsi"`
	KeyStore KeyStore `json:"keyStore"`
	// Key は置き場所の加入者。ない場合は nil（Issues に KEY_MISSING）。
	Key *Key `json:"key"`
	// Policy は認可ポリシー。ない場合は nil（Issues に POLICY_MISSING）。
	Policy *provapi.PolicyPut `json:"policy"`
	// Issues は食い違い。正常なら空。
	Issues []Issue `json:"issues"`
}

// SubscriberCreate は加入者の作成の内容。認可ポリシーは必須。
type SubscriberCreate struct {
	IMSI string `json:"imsi"`
	// KeyStore は省略すれば provisioner が PLMN マップから決める。
	KeyStore KeyStore `json:"keyStore,omitempty"`
	Ki       string   `json:"ki"`
	OPc      string   `json:"opc"`
	// AMF と SQN は省略すれば 8000、000000000000。
	AMF string `json:"amf,omitempty"`
	SQN string `json:"sqn,omitempty"`
	// SQNType と AllowPlain は aka-only-server の加入者だけ指定できる。
	SQNType    string            `json:"sqnType,omitempty"`
	AllowPlain *bool             `json:"allowPlain,omitempty"`
	Policy     provapi.PolicyPut `json:"policy"`
}

// SubscriberUpdate は加入者の変更の内容（JSON Merge Patch。空の項目は送らない）。
type SubscriberUpdate struct {
	Ki  string `json:"ki,omitempty"`
	OPc string `json:"opc,omitempty"`
	AMF string `json:"amf,omitempty"`
	SQN string `json:"sqn,omitempty"`
	// SQNType と AllowPlain は aka-only-server の加入者だけ指定できる。
	SQNType    string `json:"sqnType,omitempty"`
	AllowPlain *bool  `json:"allowPlain,omitempty"`
	// Policy は認可ポリシー全体を置き換える（ない場合は作成する）。
	Policy *provapi.PolicyPut `json:"policy,omitempty"`
}

// SubscriberList は加入者の一覧の 1 ページ。provisioner は総数を返さない。
type SubscriberList struct {
	Items []Subscriber `json:"items"`
	// NextCursor は次のページがある場合だけ入る。
	NextCursor string `json:"nextCursor"`
}

// ListSubscribers は加入者（鍵だけ・ポリシーだけの IMSI を含む）の一覧を IMSI の昇順で取得する。
func (c *Client) ListSubscribers(ctx context.Context, p provapi.ListParams) (SubscriberList, error) {
	return c.c.Call[SubscriberList](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"subscribers"}, Query: p.Values()})
}

// GetSubscriber は加入者を取得する。どこにもなければ 404（USER_NOT_FOUND）。
func (c *Client) GetSubscriber(ctx context.Context, imsi string) (Subscriber, error) {
	return c.c.Call[Subscriber](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"subscribers", imsi}})
}

// CreateSubscriber は加入者を作成する（置き場所に鍵を作り、認可ポリシーを PUT する）。
// idempotencyKey が空でなければ Idempotency-Key として送る（送り直しても二重に処理されない）。
func (c *Client) CreateSubscriber(ctx context.Context, s SubscriberCreate, idempotencyKey string) (Subscriber, error) {
	return c.c.Call[Subscriber](ctx, provapi.Request{Method: http.MethodPost, Path: []string{"subscribers"}, Body: s,
		NeedOperator: true, IdempotencyKey: idempotencyKey})
}

// UpdateSubscriber は加入者を変更する。
func (c *Client) UpdateSubscriber(ctx context.Context, imsi string, u SubscriberUpdate, idempotencyKey string) (Subscriber, error) {
	return c.c.Call[Subscriber](ctx, provapi.Request{Method: http.MethodPatch, Path: []string{"subscribers", imsi}, Body: u,
		ContentType: "application/merge-patch+json", NeedOperator: true, IdempotencyKey: idempotencyKey})
}

// DeleteSubscriber は加入者を削除する（認可ポリシーと、置き場所の鍵を消す）。
func (c *Client) DeleteSubscriber(ctx context.Context, imsi string, idempotencyKey string) error {
	_, err := c.c.Call[struct{}](ctx, provapi.Request{Method: http.MethodDelete, Path: []string{"subscribers", imsi},
		NeedOperator: true, IdempotencyKey: idempotencyKey})
	return err
}

// ---- 操作の記録 ----

// 操作の状態。
const (
	OpRunning    = "running"
	OpCompleted  = "completed"
	OpRolledBack = "rolled_back"
	OpRetrying   = "retrying"
	OpFailed     = "failed"
	OpDismissed  = "dismissed"
)

// Operation は操作の記録（2 つのノードにまたがる加入者の作成・変更・削除）。
type Operation struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	IMSI     string          `json:"imsi"`
	KeyStore KeyStore        `json:"keyStore"`
	Status   string          `json:"status"`
	Steps    []OperationStep `json:"steps"`
	// Attempts は補償・やり直しを試みた回数（最初の要求を含まない）。
	Attempts int `json:"attempts"`
	// NextAttemptAt は次に自動でやり直す時刻（retrying のときだけ）。
	NextAttemptAt time.Time `json:"nextAttemptAt,omitzero"`
	// Operator、MgmtClient、TraceID は最初の要求のもの。
	Operator   string    `json:"operator"`
	MgmtClient string    `json:"mgmtClient"`
	TraceID    string    `json:"traceId"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// OperationStep は操作の手順。
type OperationStep struct {
	Name       string     `json:"name"`
	Downstream string     `json:"downstream"`
	State      string     `json:"state"`
	Error      *StepError `json:"error"`
}

// StepError は手順の最後の失敗。
type StepError struct {
	// Status は下流の HTTP ステータス（接続できなかった場合は 0）。
	Status int       `json:"status"`
	Cause  string    `json:"cause"`
	Detail string    `json:"detail"`
	Time   time.Time `json:"time"`
}

// OperationList は未解決の操作の一覧。
type OperationList struct {
	Items []Operation `json:"items"`
	// Total は条件に一致する未解決の操作の件数（Items は limit 件まで）。
	Total int `json:"total"`
}

// ListOperations は未解決（running / retrying / failed）の操作を、作成の古い順に取得する。
// status が空でなければその状態だけ、limit が 0 なら provisioner の既定（100 件）。
func (c *Client) ListOperations(ctx context.Context, status string, limit int) (OperationList, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.c.Call[OperationList](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"operations"}, Query: q})
}

// GetOperation は操作を取得する（完了したものも保持期間のあいだ取得できる）。
func (c *Client) GetOperation(ctx context.Context, id string) (Operation, error) {
	return c.c.Call[Operation](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"operations", id}})
}

// RetryOperation は failed の操作の続きをその場で 1 回行う。
func (c *Client) RetryOperation(ctx context.Context, id, idempotencyKey string) (Operation, error) {
	return c.c.Call[Operation](ctx, provapi.Request{Method: http.MethodPost, Path: []string{"operations", id, "retry"},
		NeedOperator: true, IdempotencyKey: idempotencyKey})
}

// DismissOperation は retrying / failed の操作を、手で直した後に閉じる（下流には何もしない）。
func (c *Client) DismissOperation(ctx context.Context, id, idempotencyKey string) (Operation, error) {
	return c.c.Call[Operation](ctx, provapi.Request{Method: http.MethodPost, Path: []string{"operations", id, "dismiss"},
		NeedOperator: true, IdempotencyKey: idempotencyKey})
}

// ---- 監査ログ ----

// AuditLogEntry は provisioner の監査ログの 1 件。
type AuditLogEntry struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	// Operator は X-Operator-Id の値（自動のやり直しでは空文字）。
	Operator string `json:"operator"`
	// MgmtClient は管理クライアントの識別名（provisioner の PROVISIONER_ADMIN_CLIENTS の名前。自動のやり直しでは空文字）。
	MgmtClient string `json:"mgmtClient"`
	Action     string `json:"action"`
	// Target は対象（加入者・認可ポリシーは IMSI、RADIUSクライアントは ID、操作の記録は操作の ID）。
	Target      string `json:"target"`
	TraceID     string `json:"traceId"`
	OperationID string `json:"operationId"`
	// Result は結果（completed / rolled_back / retrying / failed / dismissed）。
	Result  string         `json:"result"`
	Details map[string]any `json:"details"`
}

// AuditLogList は provisioner の監査ログの 1 ページ。
type AuditLogList struct {
	Items      []AuditLogEntry `json:"items"`
	NextBefore string          `json:"nextBefore"`
}

// ListAuditLogs は provisioner 自身の監査ログを新しい順に取得する。
func (c *Client) ListAuditLogs(ctx context.Context, p provapi.AuditLogParams) (AuditLogList, error) {
	return c.c.Call[AuditLogList](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"audit-logs"}, Query: p.Values()})
}

// ListProvAuditLogs は provisioning-api の監査ログ（provisioner の中継。形は provisioning-api と同じ）を取得する。
func (c *Client) ListProvAuditLogs(ctx context.Context, p provapi.AuditLogParams) (provapi.AuditLogList, error) {
	return c.c.Call[provapi.AuditLogList](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"prov", "audit-logs"}, Query: p.Values()})
}

// AkaAuditLogEntry は aka-only-server の監査ログの 1 件（aka-only-server の管理API の形）。
type AkaAuditLogEntry struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	Operator   string    `json:"operator"`
	MgmtClient string    `json:"mgmtClient"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	TraceID    string    `json:"traceId"`
	// Detail は変更内容（変更なら項目ごとの {"from": …, "to": …}）。
	Detail map[string]any `json:"detail"`
}

// AkaAuditLogList は aka-only-server の監査ログの 1 ページ。
type AkaAuditLogList struct {
	Items      []AkaAuditLogEntry `json:"items"`
	NextBefore string             `json:"nextBefore"`
}

// ListAkaAuditLogs は aka-only-server の監査ログ（provisioner の中継）を取得する。
// provisioner が aka-only-server を扱わない設定なら 404（DOWNSTREAM_NOT_CONFIGURED）。
func (c *Client) ListAkaAuditLogs(ctx context.Context, p provapi.AuditLogParams) (AkaAuditLogList, error) {
	return c.c.Call[AkaAuditLogList](ctx, provapi.Request{Method: http.MethodGet, Path: []string{"aka", "audit-logs"}, Query: p.Values()})
}
