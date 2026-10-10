package provapi

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ---- 状態 ----

// Status は provisioning-api の状態。
type Status struct {
	// Version は provisioning-api のバージョン。
	Version string `json:"version"`
	// NodeName はノードの識別名（本PoC側の PROVISIONING_API_NODE_NAME）。
	NodeName        string    `json:"nodeName"`
	StartedAt       time.Time `json:"startedAt"`
	SubscriberCount int64     `json:"subscriberCount"`
	ClientCount     int64     `json:"clientCount"`
	PolicyCount     int64     `json:"policyCount"`
	// SessionCount はアクティブセッションの数。0.3.0 より前の provisioning-api は返さない（nil）。
	SessionCount *int64 `json:"sessionCount"`
}

// Status は provisioning-api の状態を取得する。
func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.call[Status](ctx, request{method: http.MethodGet, path: []string{"status"}})
}

// ---- 一覧の共通の条件 ----

// ListParams は加入者・認可ポリシーの一覧の条件。ゼロ値の項目は指定しない。
type ListParams struct {
	// Prefix は IMSI の前方一致条件。
	Prefix string
	// Cursor は前のページの NextCursor。
	Cursor string
	// Limit は 1 ページの件数（1〜500、省略時は 50）。
	Limit int
}

// Values は一覧のクエリ文字列の値を返す（provisioner の加入者の一覧も同じ引数を使う）。
func (p ListParams) Values() url.Values { return p.query() }

// Values は監査ログのクエリ文字列の値を返す（provisioner の監査ログも同じ引数を使う）。
func (p AuditLogParams) Values() url.Values {
	q := url.Values{}
	setString(q, "before", p.Before)
	setInt(q, "limit", int64(p.Limit))
	return q
}

func (p ListParams) query() url.Values {
	q := url.Values{}
	setString(q, "prefix", p.Prefix)
	setString(q, "cursor", p.Cursor)
	setInt(q, "limit", int64(p.Limit))
	return q
}

// ---- 加入者 ----

// Subscriber は加入者。Ki と OPc は含まない。
type Subscriber struct {
	IMSI string `json:"imsi"`
	AMF  string `json:"amf"`
	SQN  string `json:"sqn"`
	// CreatedAt は登録日時。値を持たない加入者（古いデータなど）ではゼロ値。
	CreatedAt time.Time `json:"createdAt,omitzero"`
}

// SubscriberCreate は加入者の登録内容。AMF と SQN は空なら Provisioning API の既定値（8000、000000000000）になる。
type SubscriberCreate struct {
	IMSI string `json:"imsi"`
	Ki   string `json:"ki"`
	OPc  string `json:"opc"`
	AMF  string `json:"amf,omitempty"`
	SQN  string `json:"sqn,omitempty"`
}

// SubscriberUpdate は加入者の変更内容（JSON Merge Patch）。nil の項目は変更しない。
// SQN を指定しなければ、認証で進んだ SQN には触れない。
type SubscriberUpdate struct {
	Ki  *string `json:"ki,omitzero"`
	OPc *string `json:"opc,omitzero"`
	AMF *string `json:"amf,omitzero"`
	SQN *string `json:"sqn,omitzero"`
}

// SubscriberKeys は加入者の Ki と OPc。
type SubscriberKeys struct {
	Ki  string `json:"ki"`
	OPc string `json:"opc"`
}

// SubscriberList は加入者の一覧の 1 ページ。
type SubscriberList struct {
	Items []Subscriber `json:"items"`
	// Total は Prefix に一致する加入者の総数。
	Total int64 `json:"total"`
	// NextCursor は次のページがある場合だけ入る。
	NextCursor string `json:"nextCursor"`
}

// ListSubscribers は加入者の一覧を IMSI の昇順で取得する。
func (c *Client) ListSubscribers(ctx context.Context, p ListParams) (SubscriberList, error) {
	return c.call[SubscriberList](ctx, request{method: http.MethodGet, path: []string{"subscribers"}, query: p.query()})
}

// CreateSubscriber は加入者を登録する。操作者が必要。
func (c *Client) CreateSubscriber(ctx context.Context, s SubscriberCreate) (Subscriber, error) {
	return c.call[Subscriber](ctx, request{
		method: http.MethodPost, path: []string{"subscribers"}, body: s, needOperator: true,
	})
}

// GetSubscriber は加入者を取得する。
func (c *Client) GetSubscriber(ctx context.Context, imsi string) (Subscriber, error) {
	return c.call[Subscriber](ctx, request{method: http.MethodGet, path: []string{"subscribers", imsi}})
}

// UpdateSubscriber は加入者を変更する。操作者が必要。
func (c *Client) UpdateSubscriber(ctx context.Context, imsi string, u SubscriberUpdate) (Subscriber, error) {
	return c.call[Subscriber](ctx, request{
		method: http.MethodPatch, path: []string{"subscribers", imsi}, body: u,
		contentType: "application/merge-patch+json", needOperator: true,
	})
}

// DeleteSubscriber は加入者を削除する。同じ IMSI の認可ポリシーは削除しない。操作者が必要。
func (c *Client) DeleteSubscriber(ctx context.Context, imsi string) error {
	_, err := c.call[struct{}](ctx, request{
		method: http.MethodDelete, path: []string{"subscribers", imsi}, needOperator: true,
	})
	return err
}

// GetSubscriberKeys は加入者の Ki と OPc を取得する。provisioning-api の監査ログに残る。操作者が必要。
func (c *Client) GetSubscriberKeys(ctx context.Context, imsi string) (SubscriberKeys, error) {
	return c.call[SubscriberKeys](ctx, request{
		method: http.MethodGet, path: []string{"subscribers", imsi, "keys"}, needOperator: true,
	})
}

// ---- RADIUSクライアント ----

// RADIUSClient は RADIUSクライアント（AP など）。共有シークレットは含まない。
type RADIUSClient struct {
	// ID はサーバー採番の ID（1 からの連番。再利用しない）。IP を変えても変わらない。
	ID     int64  `json:"id"`
	IP     string `json:"ip"`
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
}

// RADIUSClientCreate は RADIUSクライアントの登録内容。
type RADIUSClientCreate struct {
	IP     string `json:"ip"`
	Secret string `json:"secret"`
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
}

// RADIUSClientUpdate は RADIUSクライアントの変更内容（JSON Merge Patch）。nil の項目は変更しない。
type RADIUSClientUpdate struct {
	IP     *string `json:"ip,omitzero"`
	Secret *string `json:"secret,omitzero"`
	Name   *string `json:"name,omitzero"`
	Vendor *string `json:"vendor,omitzero"`
}

type radiusClientList struct {
	Items []RADIUSClient `json:"items"`
}

// ListRADIUSClients は RADIUSクライアントの全件を IP アドレスの順で取得する。
func (c *Client) ListRADIUSClients(ctx context.Context) ([]RADIUSClient, error) {
	l, err := c.call[radiusClientList](ctx, request{method: http.MethodGet, path: []string{"clients"}})
	return l.Items, err
}

// FindRADIUSClientByIP は IP アドレスで RADIUSクライアントを探す。見つからなければ ok が false。
func (c *Client) FindRADIUSClientByIP(ctx context.Context, ip string) (client RADIUSClient, ok bool, err error) {
	l, err := c.call[radiusClientList](ctx, request{
		method: http.MethodGet, path: []string{"clients"}, query: url.Values{"ip": {ip}},
	})
	if err != nil || len(l.Items) == 0 {
		return RADIUSClient{}, false, err
	}
	return l.Items[0], true, nil
}

// CreateRADIUSClient は RADIUSクライアントを登録する。ID はサーバーが採番する。操作者が必要。
func (c *Client) CreateRADIUSClient(ctx context.Context, rc RADIUSClientCreate) (RADIUSClient, error) {
	return c.call[RADIUSClient](ctx, request{
		method: http.MethodPost, path: []string{"clients"}, body: rc, needOperator: true,
	})
}

// GetRADIUSClient は RADIUSクライアントを取得する。
func (c *Client) GetRADIUSClient(ctx context.Context, id int64) (RADIUSClient, error) {
	return c.call[RADIUSClient](ctx, request{method: http.MethodGet, path: []string{"clients", formatID(id)}})
}

// UpdateRADIUSClient は RADIUSクライアントを変更する。操作者が必要。
func (c *Client) UpdateRADIUSClient(ctx context.Context, id int64, u RADIUSClientUpdate) (RADIUSClient, error) {
	return c.call[RADIUSClient](ctx, request{
		method: http.MethodPatch, path: []string{"clients", formatID(id)}, body: u,
		contentType: "application/merge-patch+json", needOperator: true,
	})
}

// DeleteRADIUSClient は RADIUSクライアントを削除する。操作者が必要。
func (c *Client) DeleteRADIUSClient(ctx context.Context, id int64) error {
	_, err := c.call[struct{}](ctx, request{
		method: http.MethodDelete, path: []string{"clients", formatID(id)}, needOperator: true,
	})
	return err
}

// GetRADIUSClientSecret は RADIUSクライアントの共有シークレットを取得する。provisioning-api の監査ログに残る。
// 操作者が必要。
func (c *Client) GetRADIUSClientSecret(ctx context.Context, id int64) (string, error) {
	s, err := c.call[struct {
		Secret string `json:"secret"`
	}](ctx, request{
		method: http.MethodGet, path: []string{"clients", formatID(id), "secret"}, needOperator: true,
	})
	return s.Secret, err
}

// ---- 認可ポリシー ----

// 認可ポリシーの既定の動作（どのルールにも一致しなかったとき）。
const (
	PolicyAllow = "allow"
	PolicyDeny  = "deny"
)

// PolicyRule は認可ポリシーのルール。先頭から順に評価し、最初に一致したものを採用する。
type PolicyRule struct {
	// NASID は NAS-Identifier。"*" は任意の NAS に一致する。
	NASID string `json:"nasId"`
	// AllowedSSIDs は許可する SSID。"*" は任意の SSID に一致する。
	AllowedSSIDs []string `json:"allowedSsids"`
	// VLANID は割り当てる VLAN ID（0〜4094 の数字）。空なら未設定。
	VLANID string `json:"vlanId,omitempty"`
	// SessionTimeout は Session-Timeout（秒）。0 なら未設定。
	SessionTimeout int `json:"sessionTimeout,omitzero"`
}

// Policy は認可ポリシー。
type Policy struct {
	IMSI    string       `json:"imsi"`
	Default string       `json:"default"`
	Rules   []PolicyRule `json:"rules"`
	// Status は加入者の状態（Provisioning API 0.4.0 から）。PolicyActive か PolicySuspended。
	// 0.3.0 以前の provisioning-api では空。Valkey を直接書き換えた不正な値は、そのまま入る（Auth Server は認証を拒否する）。
	Status string `json:"status,omitempty"`
}

// 加入者の状態（Policy.Status）。停止中の加入者の認証は、鍵の置き場所によらず Auth Server が拒否する。
const (
	PolicyActive    = "active"
	PolicySuspended = "suspended"
)

// PolicyPut は認可ポリシーの内容（全体を置き換える）。
type PolicyPut struct {
	Default string       `json:"default"`
	Rules   []PolicyRule `json:"rules"`
}

// PolicyList は認可ポリシーの一覧の 1 ページ。
type PolicyList struct {
	Items []Policy `json:"items"`
	// Total は Prefix に一致する認可ポリシーの総数。
	Total int64 `json:"total"`
	// NextCursor は次のページがある場合だけ入る。
	NextCursor string `json:"nextCursor"`
}

// ListPolicies は認可ポリシーの一覧を IMSI の昇順で取得する。
func (c *Client) ListPolicies(ctx context.Context, p ListParams) (PolicyList, error) {
	return c.call[PolicyList](ctx, request{method: http.MethodGet, path: []string{"policies"}, query: p.query()})
}

// GetPolicy は認可ポリシーを取得する。
func (c *Client) GetPolicy(ctx context.Context, imsi string) (Policy, error) {
	return c.call[Policy](ctx, request{method: http.MethodGet, path: []string{"policies", imsi}})
}

// PutPolicy は認可ポリシーを作成する、または全体を置き換える。作成した場合は created が true。
// 加入者の有無は問わない。操作者が必要。
func (c *Client) PutPolicy(ctx context.Context, imsi string, p PolicyPut) (policy Policy, created bool, err error) {
	req := request{method: http.MethodPut, path: []string{"policies", imsi}, body: p, needOperator: true}
	resp, err := c.send(ctx, req)
	if err != nil {
		return Policy{}, false, err
	}
	defer drainClose(resp.Body)
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxResponseBytes), &policy); err != nil {
		return Policy{}, false, fmt.Errorf("provisioning api PUT %s: decode response: %w", resp.Request.URL.Path, err)
	}
	return policy, resp.StatusCode == http.StatusCreated, nil
}

// SetPolicyStatus は加入者を停止する（PolicySuspended）、または再開する（PolicyActive）。変更後の認可ポリシーを返す。
// 認可ポリシーの状態だけを変える（Provisioning API 0.4.0 の PUT /policies/{imsi}/status。eapaka-node-provisioner も同じ形で中継する）。
// 認可ポリシーがなければ POLICY_NOT_FOUND。効くのは次の認証からで、接続中のセッションは切れない。操作者が必要。
func (c *Client) SetPolicyStatus(ctx context.Context, imsi, status string) (Policy, error) {
	return c.call[Policy](ctx, request{
		method: http.MethodPut, path: []string{"policies", imsi, "status"},
		body: struct {
			Status string `json:"status"`
		}{status}, needOperator: true,
	})
}

// DeletePolicy は認可ポリシーを削除する。操作者が必要。
func (c *Client) DeletePolicy(ctx context.Context, imsi string) error {
	_, err := c.call[struct{}](ctx, request{
		method: http.MethodDelete, path: []string{"policies", imsi}, needOperator: true,
	})
	return err
}

// ---- 監査ログ ----

// AuditLogParams は provisioning-api の監査ログの一覧の条件。ゼロ値の項目は指定しない。
type AuditLogParams struct {
	// Before は前のページの NextBefore（このエントリID より古いものを返す）。
	Before string
	// Limit は 1 ページの件数（1〜500、省略時は 100）。
	Limit int
}

// AuditLogEntry は provisioning-api の監査ログの 1 件（変更操作と秘密の値の取得）。
type AuditLogEntry struct {
	// ID はエントリID（Valkey の Stream の ID）。
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	// Operator は X-Operator-Id の値（省略された操作では空文字）。
	Operator string `json:"operator"`
	// MgmtClient は管理クライアントの識別名（本PoC側の PROVISIONING_API_ADMIN_CLIENTS の名前）。
	MgmtClient string `json:"mgmtClient"`
	// Action は操作（例: subscriber.create、subscriber.keys.read、client.secret.read）。
	Action string `json:"action"`
	// Target は対象（加入者・認可ポリシーは IMSI、RADIUSクライアントは ID）。
	Target    string `json:"target"`
	TargetKey string `json:"targetKey"`
	// TraceID はトレースID（BFF の監査ログの trace_id と同じ）。
	TraceID string `json:"traceId"`
	// Details は変更内容（秘密の値は含まない）。
	Details string `json:"details"`
}

// AuditLogList は provisioning-api の監査ログの 1 ページ（新しい順）。
type AuditLogList struct {
	Items []AuditLogEntry `json:"items"`
	// NextBefore はさらに古いエントリがある場合だけ入る。
	NextBefore string `json:"nextBefore"`
}

// ListAuditLogs は provisioning-api の監査ログを新しい順に取得する。
func (c *Client) ListAuditLogs(ctx context.Context, p AuditLogParams) (AuditLogList, error) {
	return c.call[AuditLogList](ctx, request{method: http.MethodGet, path: []string{"audit-logs"}, query: p.Values()})
}

// ---- セッション ----

// SessionParams はセッションの一覧の条件。ゼロ値の項目は指定しない。
type SessionParams struct {
	// IMSI を指定すると、その加入者のセッションだけを返す。
	IMSI string
	// Limit は返す件数の上限（1〜1000、省略時は 100）。
	Limit int
}

// Session はアクティブセッション。
type Session struct {
	// ID はセッションの UUID（RADIUS の Class 属性の値）。
	ID   string `json:"id"`
	IMSI string `json:"imsi"`
	// NasIP は NAS の IP アドレス。
	NasIP         string `json:"nasIp"`
	NasIdentifier string `json:"nasIdentifier"`
	// StartTime は接続開始日時。値を持たないセッションではゼロ値。
	StartTime time.Time `json:"startTime,omitzero"`
	// ClientIP は端末の IP アドレス（Accounting-Request を受けるまでは空文字）。
	ClientIP string `json:"clientIp"`
	// AcctSessionID は Acct-Session-Id（Accounting-Start を受けるまでは空文字）。
	AcctSessionID string `json:"acctSessionId"`
	InputOctets   int64  `json:"inputOctets"`
	OutputOctets  int64  `json:"outputOctets"`
}

// SessionList はセッションの一覧（接続開始の新しい順）。
type SessionList struct {
	Items []Session `json:"items"`
	// Total は条件に一致するセッションの総数（Items は Limit 件まで）。
	Total int64 `json:"total"`
}

// ListSessions はアクティブセッションを接続開始の新しい順に取得する。
func (c *Client) ListSessions(ctx context.Context, p SessionParams) (SessionList, error) {
	q := url.Values{}
	setString(q, "imsi", p.IMSI)
	setInt(q, "limit", int64(p.Limit))
	return c.call[SessionList](ctx, request{method: http.MethodGet, path: []string{"sessions"}, query: q})
}

// ---- 補助 ----

func formatID(id int64) string { return strconv.FormatInt(id, 10) }

func setString(q url.Values, name, v string) {
	if v != "" {
		q.Set(name, v)
	}
}

func setInt(q url.Values, name string, v int64) {
	if v != 0 {
		q.Set(name, strconv.FormatInt(v, 10))
	}
}
