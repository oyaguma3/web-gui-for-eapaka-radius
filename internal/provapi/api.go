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
}

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

// DeletePolicy は認可ポリシーを削除する。操作者が必要。
func (c *Client) DeletePolicy(ctx context.Context, imsi string) error {
	_, err := c.call[struct{}](ctx, request{
		method: http.MethodDelete, path: []string{"policies", imsi}, needOperator: true,
	})
	return err
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
