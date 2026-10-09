// Package provapi は本PoC（eapaka-radius-server-poc）の Provisioning API（provisioning-api）のクライアント。
// API 仕様は本PoCのリポジトリの docs/openapi/provisioning-api.yaml を参照。
//
// 接続は mTLS で、BFF のクライアント証明書を提示し、provisioning-api のサーバー証明書（自己署名）を
// 信頼する証明書として検証する（ホスト名も検証する）。
//
// 変更操作と秘密の値（Ki / OPc、共有シークレット）の取得には操作者のユーザーID が要る。
// WithOperator でコンテキストに入れておくと X-Operator-Id ヘッダーで渡す。入っていなければ ErrNoOperator を返し、
// リクエストを送らない。
//
// トレースID（internal/trace）がコンテキストに入っていれば X-Trace-ID ヘッダーで渡す。入っていなければ呼び出しごとに採番する。
//
// eapaka-node-provisioner（以下「provisioner」）は、RADIUSクライアント・認可ポリシー・セッション・鍵の取得を
// Provisioning API と同じ形で中継する。接続先を provisioner にした Client で、それらのメソッドをそのまま使える。
// provisioner だけの部分は internal/pvapi が Call で呼ぶ。
package provapi

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/trace"
)

const (
	defaultTimeout = 10 * time.Second
	// maxResponseBytes は応答ボディを読む上限。一覧の最大（500 件）でも収まる大きさにする。
	maxResponseBytes     = 16 << 20
	operatorHeader       = "X-Operator-Id"
	traceHeader          = "X-Trace-ID"
	idempotencyKeyHeader = "Idempotency-Key"
	// defaultName はログとエラーに出す API の名前の既定。
	defaultName = "provisioning api"
)

// operatorPattern は X-Operator-Id の形式。provisioning-api 側の検証と同じ。
var operatorPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

var (
	// ErrNoOperator は、操作者が必要な操作でコンテキストに操作者が入っていないことを表す。
	ErrNoOperator = errors.New("provapi: operator id is required for this operation")
	// ErrInvalidOperator は、操作者のユーザーID が X-Operator-Id の形式に合わないことを表す。
	ErrInvalidOperator = errors.New("provapi: operator id does not match " + operatorPattern.String())
)

// Options はクライアントの設定。
type Options struct {
	// BaseURL は Provisioning API のベース URL（例: https://provisioning-api:9444/admin/v1）。
	BaseURL string
	// ClientCertFile は BFF のクライアント証明書（PEM）。
	ClientCertFile string
	// ClientKeyFile はクライアント証明書の秘密鍵（PEM）。空なら ClientCertFile から読む
	// （gen-client-cert は、出力先を分けなければ証明書と秘密鍵を 1 つの PEM にまとめて出力する）。
	ClientKeyFile string
	// ServerCertFile は provisioning-api のサーバー証明書（PEM）。これを信頼する証明書として検証する。
	ServerCertFile string
	// Timeout は 1 回の呼び出しの上限時間。0 なら 10 秒。
	Timeout time.Duration
	// Name はログとエラーに出す API の名前（例: provisioner）。空なら "provisioning api"。
	Name string
	Log  *slog.Logger
}

// Client は Provisioning API のクライアント。
type Client struct {
	base *url.URL
	hc   *http.Client
	log  *slog.Logger
	name string
	// clientCert は提示するクライアント証明書。フィンガープリントの表示に使う。
	clientCert *x509.Certificate
}

// New はクライアントを作る。証明書のファイルはここで読み込む。
func New(opts Options) (*Client, error) {
	base, err := url.Parse(opts.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("provisioning api url %q: want https://host:port/path", opts.BaseURL)
	}
	cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, cmp.Or(opts.ClientKeyFile, opts.ClientCertFile))
	if err != nil {
		return nil, fmt.Errorf("load provisioning api client certificate: %w", err)
	}
	serverPEM, err := os.ReadFile(opts.ServerCertFile)
	if err != nil {
		return nil, fmt.Errorf("load provisioning api server certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(serverPEM) {
		return nil, fmt.Errorf("load provisioning api server certificate: no PEM certificate in %s", opts.ServerCertFile)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
			RootCAs:      roots,
		},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 8,
	}
	return &Client{
		base: base,
		hc: &http.Client{
			Transport: tr,
			Timeout:   cmp.Or(opts.Timeout, defaultTimeout),
			// Provisioning API はリダイレクトを返さない。返ってきたらそのまま応答として扱う。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		log:        opts.Log,
		name:       cmp.Or(opts.Name, defaultName),
		clientCert: cert.Leaf,
	}, nil
}

// ClientCertificate は Provisioning API に提示するクライアント証明書を返す。
func (c *Client) ClientCertificate() *x509.Certificate { return c.clientCert }

// BaseURL は Provisioning API のベース URL を返す。
func (c *Client) BaseURL() string { return c.base.String() }

// ---- 操作者 ----

type operatorKey struct{}

// WithOperator は、Provisioning API に渡す操作者のユーザーID をコンテキストに入れる。
func WithOperator(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, operatorKey{}, userID)
}

// OperatorFrom はコンテキストに入れた操作者のユーザーID を返す。
func OperatorFrom(ctx context.Context) string {
	id, _ := ctx.Value(operatorKey{}).(string)
	return id
}

// ---- エラー ----

// ProblemDetails の cause の値。
const (
	CauseInvalidMsgFormat     = "INVALID_MSG_FORMAT"
	CauseInvalidQueryParam    = "INVALID_QUERY_PARAM"
	CauseMandatoryIEMissing   = "MANDATORY_IE_MISSING"
	CauseMandatoryIEIncorrect = "MANDATORY_IE_INCORRECT"
	CauseOptionalIEIncorrect  = "OPTIONAL_IE_INCORRECT"
	CauseUserNotFound         = "USER_NOT_FOUND"
	CauseClientNotFound       = "CLIENT_NOT_FOUND"
	CausePolicyNotFound       = "POLICY_NOT_FOUND"
	CauseSubscriberExists     = "SUBSCRIBER_ALREADY_EXISTS"
	CauseClientExists         = "CLIENT_ALREADY_EXISTS"
	CauseSystemFailure        = "SYSTEM_FAILURE"
)

// InvalidParam は不正だった項目。
type InvalidParam struct {
	// Param は項目の名前（例: ki、rules[0].allowedSsids[1]）。
	Param  string `json:"param"`
	Reason string `json:"reason,omitempty"`
}

// Problem は Provisioning API のエラー応答（ProblemDetails）。
// Downstream 以降は provisioner の拡張項目（provisioning-api は返さない）。
type Problem struct {
	Title         string         `json:"title,omitempty"`
	Status        int            `json:"status,omitzero"`
	Detail        string         `json:"detail,omitempty"`
	Cause         string         `json:"cause,omitempty"`
	InvalidParams []InvalidParam `json:"invalidParams,omitempty"`

	// Downstream はエラーの元の下流（prov / aka）。
	Downstream string `json:"downstream,omitempty"`
	// DownstreamStatus と DownstreamCause は下流の HTTP ステータスと cause。
	DownstreamStatus int    `json:"downstreamStatus,omitzero"`
	DownstreamCause  string `json:"downstreamCause,omitempty"`
	// OperationID は操作の記録の ID（書き込みの途中の失敗、または同じ IMSI の未完了の操作）。
	OperationID string `json:"operationId,omitempty"`
	// RolledBack は、補償で元に戻したか（OperationID がある場合だけ）。
	RolledBack *bool `json:"rolledBack,omitempty"`
	// Conflicts は、SUBSCRIBER_ALREADY_EXISTS で同じ IMSI のものがあった場所（poc / aka / policy）。
	Conflicts []string `json:"conflicts,omitempty"`
}

// Error は Provisioning API がエラーを返したことを表す。
// 応答が ProblemDetails でなかった場合、Problem は空になる。
type Error struct {
	// API は API の名前（Options.Name）。
	API    string
	Method string
	Path   string
	Status int
	// TraceID は provisioning-api が応答の X-Trace-ID で返したトレースID。
	TraceID string
	Problem Problem
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s %s %s: %d %s", cmp.Or(e.API, defaultName), e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Problem.Cause != "" {
		msg += " (" + e.Problem.Cause + ")"
	}
	if e.Problem.Detail != "" {
		msg += ": " + e.Problem.Detail
	}
	return msg
}

// CauseOf は、err が Provisioning API のエラー応答なら cause を返す。そうでなければ空文字列。
func CauseOf(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Problem.Cause
	}
	return ""
}

// IsUnavailable は、err が Provisioning API に届かなかった（接続・TLS・タイムアウトなど）ことを表すかを返す。
// Provisioning API が応答を返した場合と、呼び出し側の誤り（操作者の指定漏れなど）は false。
func IsUnavailable(err error) bool {
	if err == nil || errors.Is(err, ErrNoOperator) || errors.Is(err, ErrInvalidOperator) {
		return false
	}
	_, isAPIError := errors.AsType[*Error](err)
	return !isAPIError
}

// ---- 呼び出し ----

// request は 1 回の呼び出しの内容。
type request struct {
	method string
	// path は BaseURL からの相対パスの要素。各要素はエスケープして連結する。
	path  []string
	query url.Values
	// body は JSON にして送る値。nil なら送らない。
	body        any
	contentType string
	// needOperator が true なら、コンテキストに操作者が入っていなければ送らない。
	needOperator bool
	// idempotencyKey は Idempotency-Key ヘッダーの値（provisioner だけが使う）。空なら付けない。
	idempotencyKey string
}

// Request は Call に渡す 1 回の呼び出しの内容。
type Request struct {
	Method string
	// Path は BaseURL からの相対パスの要素。
	Path  []string
	Query url.Values
	// Body は JSON にして送る値。nil なら送らない。
	Body any
	// ContentType は Body の Content-Type。空なら application/json。
	ContentType string
	// NeedOperator が true なら、コンテキストに操作者が入っていなければ送らない（ErrNoOperator）。
	NeedOperator bool
	// IdempotencyKey は Idempotency-Key ヘッダーの値。空なら付けない。
	IdempotencyKey string
}

// Call は API を呼び出し、2xx の応答ボディを T として読む。T が struct{} ならボディは読まない。
// 2xx 以外の応答は *Error を返す。このパッケージにない操作（provisioner だけの API）を呼ぶのに使う。
func (c *Client) Call[T any](ctx context.Context, r Request) (T, error) {
	return c.call[T](ctx, request{method: r.Method, path: r.Path, query: r.Query, body: r.Body,
		contentType: r.ContentType, needOperator: r.NeedOperator, idempotencyKey: r.IdempotencyKey})
}

// call は Provisioning API を呼び出し、2xx の応答ボディを T として読む。T が struct{} ならボディは読まない。
func (c *Client) call[T any](ctx context.Context, req request) (T, error) {
	var out T
	resp, err := c.send(ctx, req)
	if err != nil {
		return out, err
	}
	defer drainClose(resp.Body)

	if _, noBody := any(out).(struct{}); noBody {
		return out, nil
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxResponseBytes), &out); err != nil {
		return out, fmt.Errorf("%s %s %s: decode response: %w", c.name, req.method, resp.Request.URL.Path, err)
	}
	return out, nil
}

// send はリクエストを送り、2xx の応答を返す。それ以外の応答は *Error にして返す。
func (c *Client) send(ctx context.Context, req request) (*http.Response, error) {
	op := OperatorFrom(ctx)
	switch {
	case op != "" && !operatorPattern.MatchString(op):
		return nil, ErrInvalidOperator
	case op == "" && req.needOperator:
		return nil, ErrNoOperator
	}
	traceID := cmp.Or(trace.From(ctx), trace.New())

	u := c.base.JoinPath(req.path...)
	u.RawQuery = req.query.Encode()

	var body io.Reader
	if req.body != nil {
		b, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("%s %s %s: encode request: %w", c.name, req.method, u.Path, err)
		}
		body = bytes.NewReader(b)
	}
	hreq, err := http.NewRequestWithContext(ctx, req.method, u.String(), body)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Accept", "application/json, application/problem+json")
	if req.body != nil {
		hreq.Header.Set("Content-Type", cmp.Or(req.contentType, "application/json"))
	}
	if op != "" {
		hreq.Header.Set(operatorHeader, op)
	}
	hreq.Header.Set(traceHeader, traceID)
	if req.idempotencyKey != "" {
		hreq.Header.Set(idempotencyKeyHeader, req.idempotencyKey)
	}

	start := time.Now()
	resp, err := c.hc.Do(hreq)
	if err != nil {
		c.log.Warn("provisioning api call failed", "api", c.name, "method", req.method, "path", u.Path,
			"trace_id", traceID, "error", err)
		return nil, fmt.Errorf("%s %s %s: %w", c.name, req.method, u.Path, err)
	}
	// provisioning-api は使ったトレースID を応答で返す（送った値と同じになるはず）。
	traceID = cmp.Or(resp.Header.Get(traceHeader), traceID)
	// リクエストとレスポンスのボディ、クエリ文字列は出さない（Ki / OPc や検索条件を含みうるため）。
	c.log.Debug("provisioning api call", "api", c.name, "method", req.method, "path", u.Path, "status", resp.StatusCode,
		"duration_ms", time.Since(start).Milliseconds(), "operator", op, "trace_id", traceID)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer drainClose(resp.Body)
	apiErr := &Error{API: c.name, Method: req.method, Path: u.Path, Status: resp.StatusCode, TraceID: traceID}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/problem+json" {
		// 読めなくても、ステータスコードだけで扱えるようにする。
		_ = json.UnmarshalRead(io.LimitReader(resp.Body, maxResponseBytes), &apiErr.Problem)
	}
	return nil, apiErr
}

// drainClose は、接続を再利用できるよう残りのボディを読み捨ててから閉じる。
func drainClose(body io.ReadCloser) {
	io.Copy(io.Discard, io.LimitReader(body, maxResponseBytes))
	body.Close()
}
