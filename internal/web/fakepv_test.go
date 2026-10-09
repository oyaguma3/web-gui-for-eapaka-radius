package web

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// fakePV はテスト用の eapaka-node-provisioner（provisioner だけの API）。加入者と操作の記録をメモリに持ち、
// 変更操作ごとに「操作名 操作者 Idempotency-Key」を記録する。
type fakePV struct {
	mu     sync.Mutex
	status pvapi.Status
	// statusCalls は Status の呼び出し回数（状態の写しの使い回しを確かめる）。
	statusCalls int
	subs        map[string]pvapi.Subscriber
	ops         map[string]pvapi.Operation
	audit       []pvapi.AuditLogEntry
	provAudit   []provapi.AuditLogEntry
	akaAudit    []pvapi.AkaAuditLogEntry
	// errs は操作名ごとに返すエラー（GetSubscriber、CreateSubscriber など）。
	errs  map[string]error
	calls []string
	// lastCreate と lastUpdate は最後に受け取った作成・変更の内容。
	lastCreate pvapi.SubscriberCreate
	lastUpdate pvapi.SubscriberUpdate
	// retryResult は RetryOperation の後の状態（空なら completed）。
	retryResult string
}

func newFakePV() *fakePV {
	return &fakePV{
		status: pvapi.Status{Version: "0.2.0", PLMNMap: []pvapi.PLMNEntry{{PLMN: "00102", KeyStore: pvapi.KeyStoreAKA}},
			Downstreams: pvapi.Downstreams{
				Prov: pvapi.DownstreamStatus{Configured: true, Reachable: true, Version: "0.3.0", NodeName: "poc-01", SubscriberCount: 3},
				Aka:  pvapi.DownstreamStatus{Configured: true, Reachable: true, Version: "1.0.0", SubscriberCount: 2},
			},
			AVClient:   pvapi.AVClientStatus{ID: 1, Exists: true, Enabled: true, Name: "vector-gateway"},
			Valkey:     pvapi.ValkeyStatus{Reachable: true},
			Operations: &pvapi.OperationCounts{}},
		subs: map[string]pvapi.Subscriber{},
		ops:  map[string]pvapi.Operation{},
		errs: map[string]error{},
	}
}

func (f *fakePV) record(ctx context.Context, op, key string) {
	f.calls = append(f.calls, strings.TrimSpace(op+" "+provapi.OperatorFrom(ctx)+" "+key))
}

// lastCall は最後の記録を返す。
func (f *fakePV) lastCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakePV) Status(context.Context) (pvapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.status, f.errs["Status"]
}

// keyStoreOf は PLMN マップから置き場所を決める（00102 は aka、それ以外は poc）。
func keyStoreOf(imsi string) pvapi.KeyStore {
	if strings.HasPrefix(imsi, "00102") {
		return pvapi.KeyStoreAKA
	}
	return pvapi.KeyStorePoC
}

func (f *fakePV) ListSubscribers(_ context.Context, p provapi.ListParams) (pvapi.SubscriberList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["ListSubscribers"]; err != nil {
		return pvapi.SubscriberList{}, err
	}
	var l pvapi.SubscriberList
	for _, imsi := range slices.Sorted(maps.Keys(f.subs)) {
		if strings.HasPrefix(imsi, p.Prefix) && imsi > p.Cursor {
			l.Items = append(l.Items, f.subs[imsi])
		}
	}
	if limit := cmp.Or(p.Limit, 50); len(l.Items) > limit {
		l.Items = l.Items[:limit]
		l.NextCursor = l.Items[limit-1].IMSI
	}
	return l, nil
}

func (f *fakePV) GetSubscriber(_ context.Context, imsi string) (pvapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["GetSubscriber"]; err != nil {
		return pvapi.Subscriber{}, err
	}
	s, ok := f.subs[imsi]
	if !ok {
		return pvapi.Subscriber{}, notFound(provapi.CauseUserNotFound)
	}
	return s, nil
}

func (f *fakePV) CreateSubscriber(ctx context.Context, in pvapi.SubscriberCreate, key string) (pvapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, "CreateSubscriber", key)
	f.lastCreate = in
	if err := f.errs["CreateSubscriber"]; err != nil {
		return pvapi.Subscriber{}, err
	}
	if _, ok := f.subs[in.IMSI]; ok {
		return pvapi.Subscriber{}, conflict(provapi.CauseSubscriberExists)
	}
	ks := keyStoreOf(in.IMSI)
	k := &pvapi.Key{AMF: in.AMF, SQN: in.SQN}
	if ks == pvapi.KeyStoreAKA {
		k.SQNType, k.AllowPlain, k.AllowedClientIDs = "inc32", new(false), []int64{1}
	}
	s := pvapi.Subscriber{IMSI: in.IMSI, KeyStore: ks, Key: k, Policy: &in.Policy, Issues: []pvapi.Issue{}}
	f.subs[in.IMSI] = s
	return s, nil
}

func (f *fakePV) UpdateSubscriber(ctx context.Context, imsi string, u pvapi.SubscriberUpdate, key string) (pvapi.Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, "UpdateSubscriber", key)
	f.lastUpdate = u
	if err := f.errs["UpdateSubscriber"]; err != nil {
		return pvapi.Subscriber{}, err
	}
	s, ok := f.subs[imsi]
	if !ok || s.Key == nil {
		return pvapi.Subscriber{}, notFound(provapi.CauseUserNotFound)
	}
	k := *s.Key
	k.AMF, k.SQN, k.SQNType = cmp.Or(u.AMF, k.AMF), cmp.Or(u.SQN, k.SQN), cmp.Or(u.SQNType, k.SQNType)
	if u.AllowPlain != nil {
		k.AllowPlain = new(*u.AllowPlain)
	}
	s.Key = &k
	f.subs[imsi] = s
	return s, nil
}

func (f *fakePV) DeleteSubscriber(ctx context.Context, imsi, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, "DeleteSubscriber", key)
	if err := f.errs["DeleteSubscriber"]; err != nil {
		return err
	}
	if _, ok := f.subs[imsi]; !ok {
		return notFound(provapi.CauseUserNotFound)
	}
	delete(f.subs, imsi)
	return nil
}

func (f *fakePV) ListOperations(_ context.Context, status string, limit int) (pvapi.OperationList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["ListOperations"]; err != nil {
		return pvapi.OperationList{}, err
	}
	var l pvapi.OperationList
	for _, id := range slices.Sorted(maps.Keys(f.ops)) {
		op := f.ops[id]
		if (status == "" && slices.Contains(operationStatuses, op.Status)) || op.Status == status {
			l.Total++
			if len(l.Items) < limit {
				l.Items = append(l.Items, op)
			}
		}
	}
	return l, nil
}

func (f *fakePV) GetOperation(_ context.Context, id string) (pvapi.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return pvapi.Operation{}, notFound(pvapi.CauseOperationNotFound)
	}
	return op, nil
}

func (f *fakePV) RetryOperation(ctx context.Context, id, key string) (pvapi.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, "RetryOperation", key)
	if err := f.errs["RetryOperation"]; err != nil {
		return pvapi.Operation{}, err
	}
	op := f.ops[id]
	op.Status = cmp.Or(f.retryResult, pvapi.OpCompleted)
	f.ops[id] = op
	return op, nil
}

func (f *fakePV) DismissOperation(ctx context.Context, id, key string) (pvapi.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(ctx, "DismissOperation", key)
	op := f.ops[id]
	op.Status = pvapi.OpDismissed
	f.ops[id] = op
	return op, nil
}

func (f *fakePV) ListAuditLogs(context.Context, provapi.AuditLogParams) (pvapi.AuditLogList, error) {
	return pvapi.AuditLogList{Items: f.audit}, f.errs["ListAuditLogs"]
}

func (f *fakePV) ListProvAuditLogs(context.Context, provapi.AuditLogParams) (provapi.AuditLogList, error) {
	return provapi.AuditLogList{Items: f.provAudit}, f.errs["ListProvAuditLogs"]
}

func (f *fakePV) ListAkaAuditLogs(context.Context, provapi.AuditLogParams) (pvapi.AkaAuditLogList, error) {
	return pvapi.AkaAuditLogList{Items: f.akaAudit}, f.errs["ListAkaAuditLogs"]
}
