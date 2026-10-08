package web

import (
	"context"
	"sync"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/provapi"
)

// fakeProv はテスト用の Provisioning API。
// err を設定すると、全ての呼び出しがそのエラーを返す。
type fakeProv struct {
	mu     sync.Mutex
	status provapi.Status
	err    error
}

func newFakeProv() *fakeProv {
	return &fakeProv{status: provapi.Status{Version: "0.2.0", NodeName: "poc-01"}}
}

func (f *fakeProv) Status(context.Context) (provapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.err
}
