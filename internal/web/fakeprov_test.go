package web

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// fakeProv はテスト用の Provisioning API。加入者・RADIUSクライアント・認可ポリシーをメモリに持ち、
// 変更操作と秘密の値の取得ごとに「操作名 操作者」を記録する。
// err を設定すると、全ての呼び出しがそのエラーを返す。
type fakeProv struct {
	mu       sync.Mutex
	status   provapi.Status
	err      error
	subs     map[string]provapi.Subscriber
	keys     map[string]provapi.SubscriberKeys
	clients  map[int64]provapi.RADIUSClient
	secrets  map[int64]string
	nextID   int64
	policies map[string]provapi.Policy
	// calls は「操作名 操作者」の記録。
	calls []string
	// lastSubUpdate と lastClientUpdate は、最後に受け取った変更の内容。
	lastSubUpdate    provapi.SubscriberUpdate
	lastClientUpdate provapi.RADIUSClientUpdate
	lastPolicyPut    provapi.PolicyPut
	// putPolicyErr を設定すると、PutPolicy だけがそのエラーを返す。
	putPolicyErr error
}

func newFakeProv() *fakeProv {
	return &fakeProv{
		status:   provapi.Status{Version: "0.2.0", NodeName: "poc-01"},
		subs:     map[string]provapi.Subscriber{},
		keys:     map[string]provapi.SubscriberKeys{},
		clients:  map[int64]provapi.RADIUSClient{},
		secrets:  map[int64]string{},
		nextID:   1,
		policies: map[string]provapi.Policy{},
	}
}

func notFound(cause string) error {
	return &provapi.Error{Status: 404, Problem: provapi.Problem{Status: 404, Cause: cause}}
}

func conflict(cause string) error {
	return &provapi.Error{Status: 409, Problem: provapi.Problem{Status: 409, Cause: cause}}
}

func (f *fakeProv) call(ctx context.Context, op string) {
	f.calls = append(f.calls, op+" "+provapi.OperatorFrom(ctx))
}

// lastCall は最後の記録を返す。
func (f *fakeProv) lastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeProv) Status(context.Context) (provapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.err
}

// ---- 加入者 ----

func (f *fakeProv) ListSubscribers(_ context.Context, p provapi.ListParams) (provapi.SubscriberList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return provapi.SubscriberList{}, f.err
	}
	var l provapi.SubscriberList
	var rest []provapi.Subscriber
	for _, imsi := range slices.Sorted(maps.Keys(f.subs)) {
		if strings.HasPrefix(imsi, p.Prefix) {
			l.Total++
			if imsi > p.Cursor {
				rest = append(rest, f.subs[imsi])
			}
		}
	}
	// cursor は前のページの最後の IMSI とする（本物の Provisioning API の cursor の中身とは違う）。
	limit := cmp.Or(p.Limit, 50)
	if len(rest) > limit {
		rest = rest[:limit]
		l.NextCursor = rest[limit-1].IMSI
	}
	l.Items = rest
	return l, nil
}

func (f *fakeProv) CreateSubscriber(ctx context.Context, s provapi.SubscriberCreate) (provapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "CreateSubscriber")
	if f.err != nil {
		return provapi.Subscriber{}, f.err
	}
	if _, ok := f.subs[s.IMSI]; ok {
		return provapi.Subscriber{}, conflict(provapi.CauseSubscriberExists)
	}
	sub := provapi.Subscriber{IMSI: s.IMSI, AMF: s.AMF, SQN: s.SQN, CreatedAt: time.Now()}
	f.subs[s.IMSI] = sub
	f.keys[s.IMSI] = provapi.SubscriberKeys{Ki: s.Ki, OPc: s.OPc}
	return sub, nil
}

func (f *fakeProv) GetSubscriber(_ context.Context, imsi string) (provapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return provapi.Subscriber{}, f.err
	}
	s, ok := f.subs[imsi]
	if !ok {
		return provapi.Subscriber{}, notFound(provapi.CauseUserNotFound)
	}
	return s, nil
}

func (f *fakeProv) UpdateSubscriber(ctx context.Context, imsi string, u provapi.SubscriberUpdate) (provapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "UpdateSubscriber")
	f.lastSubUpdate = u
	if f.err != nil {
		return provapi.Subscriber{}, f.err
	}
	s, ok := f.subs[imsi]
	if !ok {
		return provapi.Subscriber{}, notFound(provapi.CauseUserNotFound)
	}
	k := f.keys[imsi]
	if u.Ki != nil {
		k.Ki = *u.Ki
	}
	if u.OPc != nil {
		k.OPc = *u.OPc
	}
	if u.AMF != nil {
		s.AMF = *u.AMF
	}
	if u.SQN != nil {
		s.SQN = *u.SQN
	}
	f.subs[imsi], f.keys[imsi] = s, k
	return s, nil
}

func (f *fakeProv) DeleteSubscriber(ctx context.Context, imsi string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "DeleteSubscriber")
	if f.err != nil {
		return f.err
	}
	if _, ok := f.subs[imsi]; !ok {
		return notFound(provapi.CauseUserNotFound)
	}
	delete(f.subs, imsi)
	delete(f.keys, imsi)
	return nil
}

func (f *fakeProv) GetSubscriberKeys(ctx context.Context, imsi string) (provapi.SubscriberKeys, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "GetSubscriberKeys")
	if f.err != nil {
		return provapi.SubscriberKeys{}, f.err
	}
	k, ok := f.keys[imsi]
	if !ok {
		return provapi.SubscriberKeys{}, notFound(provapi.CauseUserNotFound)
	}
	return k, nil
}

// ---- RADIUSクライアント ----

func (f *fakeProv) ListRADIUSClients(context.Context) ([]provapi.RADIUSClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return slices.SortedFunc(maps.Values(f.clients), func(a, b provapi.RADIUSClient) int { return cmp.Compare(a.IP, b.IP) }), nil
}

func (f *fakeProv) ipUsed(ip string, except int64) bool {
	for id, c := range f.clients {
		if c.IP == ip && id != except {
			return true
		}
	}
	return false
}

func (f *fakeProv) CreateRADIUSClient(ctx context.Context, rc provapi.RADIUSClientCreate) (provapi.RADIUSClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "CreateRADIUSClient")
	if f.err != nil {
		return provapi.RADIUSClient{}, f.err
	}
	if f.ipUsed(rc.IP, 0) {
		return provapi.RADIUSClient{}, conflict(provapi.CauseClientExists)
	}
	c := provapi.RADIUSClient{ID: f.nextID, IP: rc.IP, Name: rc.Name, Vendor: rc.Vendor}
	f.nextID++
	f.clients[c.ID], f.secrets[c.ID] = c, rc.Secret
	return c, nil
}

func (f *fakeProv) GetRADIUSClient(_ context.Context, id int64) (provapi.RADIUSClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return provapi.RADIUSClient{}, f.err
	}
	c, ok := f.clients[id]
	if !ok {
		return provapi.RADIUSClient{}, notFound(provapi.CauseClientNotFound)
	}
	return c, nil
}

func (f *fakeProv) UpdateRADIUSClient(ctx context.Context, id int64, u provapi.RADIUSClientUpdate) (provapi.RADIUSClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "UpdateRADIUSClient")
	f.lastClientUpdate = u
	if f.err != nil {
		return provapi.RADIUSClient{}, f.err
	}
	c, ok := f.clients[id]
	if !ok {
		return provapi.RADIUSClient{}, notFound(provapi.CauseClientNotFound)
	}
	if u.IP != nil {
		if f.ipUsed(*u.IP, id) {
			return provapi.RADIUSClient{}, conflict(provapi.CauseClientExists)
		}
		c.IP = *u.IP
	}
	if u.Name != nil {
		c.Name = *u.Name
	}
	if u.Vendor != nil {
		c.Vendor = *u.Vendor
	}
	if u.Secret != nil {
		f.secrets[id] = *u.Secret
	}
	f.clients[id] = c
	return c, nil
}

func (f *fakeProv) DeleteRADIUSClient(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "DeleteRADIUSClient")
	if f.err != nil {
		return f.err
	}
	if _, ok := f.clients[id]; !ok {
		return notFound(provapi.CauseClientNotFound)
	}
	delete(f.clients, id)
	delete(f.secrets, id)
	return nil
}

func (f *fakeProv) GetRADIUSClientSecret(ctx context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "GetRADIUSClientSecret")
	if f.err != nil {
		return "", f.err
	}
	s, ok := f.secrets[id]
	if !ok {
		return "", notFound(provapi.CauseClientNotFound)
	}
	return s, nil
}

// ---- 認可ポリシー ----

func (f *fakeProv) ListPolicies(_ context.Context, p provapi.ListParams) (provapi.PolicyList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return provapi.PolicyList{}, f.err
	}
	var l provapi.PolicyList
	for _, imsi := range slices.Sorted(maps.Keys(f.policies)) {
		if strings.HasPrefix(imsi, p.Prefix) {
			l.Total++
			l.Items = append(l.Items, f.policies[imsi])
		}
	}
	return l, nil
}

func (f *fakeProv) GetPolicy(_ context.Context, imsi string) (provapi.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return provapi.Policy{}, f.err
	}
	p, ok := f.policies[imsi]
	if !ok {
		return provapi.Policy{}, notFound(provapi.CausePolicyNotFound)
	}
	return p, nil
}

func (f *fakeProv) PutPolicy(ctx context.Context, imsi string, p provapi.PolicyPut) (provapi.Policy, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "PutPolicy")
	f.lastPolicyPut = p
	if err := cmp.Or(f.err, f.putPolicyErr); err != nil {
		return provapi.Policy{}, false, err
	}
	_, exists := f.policies[imsi]
	saved := provapi.Policy{IMSI: imsi, Default: p.Default, Rules: p.Rules}
	f.policies[imsi] = saved
	return saved, !exists, nil
}

func (f *fakeProv) DeletePolicy(ctx context.Context, imsi string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "DeletePolicy")
	if f.err != nil {
		return f.err
	}
	if _, ok := f.policies[imsi]; !ok {
		return notFound(provapi.CausePolicyNotFound)
	}
	delete(f.policies, imsi)
	return nil
}
