package web

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/pvapi"
)

// pvStatusTTL は、provisioner の状態の写しを使い回す時間。
const pvStatusTTL = time.Minute

// pvStatusCache は、provisioner の状態（PLMN マップ、aka-only-server の設定の有無）の写し。
// 登録フォームの PLMN マップの表示や、監査ログのタブ（aka-only-server を扱うか）に使う。
// provisioner の /status は下流 2 つへの接続を確かめるので、下流が止まっていると時間がかかる。
// これらは設定でしか変わらないので、画面のたびには取り直さない。
type pvStatusCache struct {
	pv  PVAPI
	log *slog.Logger

	mu sync.Mutex
	st pvapi.Status
	at time.Time
}

// refresh は provisioner の状態を取り直し、写しを更新する。
func (c *pvStatusCache) refresh(ctx context.Context) (pvapi.Status, error) {
	st, err := c.pv.Status(ctx)
	if err != nil {
		return pvapi.Status{}, err
	}
	c.mu.Lock()
	c.st, c.at = st, time.Now()
	c.mu.Unlock()
	return st, nil
}

// get は provisioner の状態の写しを返す。古ければ取り直し、取り直せなければ古い写しを返す。
// 一度も取得できていなければ false。
func (c *pvStatusCache) get(ctx context.Context) (pvapi.Status, bool) {
	c.mu.Lock()
	st, at := c.st, c.at
	c.mu.Unlock()
	if !at.IsZero() && time.Since(at) < pvStatusTTL {
		return st, true
	}
	fresh, err := c.refresh(ctx)
	if err != nil {
		c.log.Warn("get provisioner status", "error", err)
		return st, !at.IsZero()
	}
	return fresh, true
}
