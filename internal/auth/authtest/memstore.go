// Package authtest はテスト用の補助を提供する。
package authtest

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/oyaguma3/web-gui-for-eapaka-radius/internal/store"
)

// MemStore はテスト用のメモリ上のストア。auth.Store を満たす。有効期限は扱わない。
type MemStore struct {
	mu       sync.Mutex
	Users    map[string]store.User
	Sessions map[string]store.Session
	Failures map[string]int64
	Audit    []store.AuditEntry
}

// NewMemStore は空の MemStore を作る。
func NewMemStore() *MemStore {
	return &MemStore{Users: map[string]store.User{}, Sessions: map[string]store.Session{}, Failures: map[string]int64{}}
}

func (m *MemStore) CreateUser(_ context.Context, u store.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Users[u.ID]; ok {
		return store.ErrExists
	}
	m.Users[u.ID] = u
	return nil
}

func (m *MemStore) GetUser(_ context.Context, id string) (store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.Users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

func (m *MemStore) ListUsers(context.Context) ([]store.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.User
	for _, id := range slices.Sorted(maps.Keys(m.Users)) {
		out = append(out, m.Users[id])
	}
	return out, nil
}

func (m *MemStore) DeleteUser(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.Users[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.Users, id)
	return nil
}

func (m *MemStore) SetPassword(_ context.Context, id, hash, stamp string, mustChange bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.Users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.PasswordHash, u.Stamp, u.MustChangePassword = hash, stamp, mustChange
	m.Users[id] = u
	return nil
}

func (m *MemStore) CreateSession(_ context.Context, idHash string, s store.Session, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sessions[idHash] = s
	return nil
}

func (m *MemStore) GetSession(_ context.Context, idHash string, _ time.Duration) (store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.Sessions[idHash]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return s, nil
}

func (m *MemStore) DeleteSession(_ context.Context, idHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Sessions, idHash)
	return nil
}

func (m *MemStore) LoginFailures(_ context.Context, id string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Failures[id], nil
}

func (m *MemStore) AddLoginFailure(_ context.Context, id string, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Failures[id]++
	return m.Failures[id], nil
}

func (m *MemStore) ClearLoginFailures(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Failures, id)
	return nil
}

func (m *MemStore) AppendAudit(_ context.Context, e store.AuditEntry, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Audit = append(m.Audit, e)
	return nil
}

// LastAudit は最後に記録した監査ログを返す。
func (m *MemStore) LastAudit() store.AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Audit[len(m.Audit)-1]
}

// ListAudit は監査ログを新しい順に返す。エントリID は記録順の連番（"1", "2", ...）とする。
func (m *MemStore) ListAudit(_ context.Context, before string, limit int) ([]store.AuditEntry, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	end := len(m.Audit)
	if before != "" {
		n, err := strconv.Atoi(before)
		if err != nil {
			return nil, "", err
		}
		end = n - 1
	}
	var out []store.AuditEntry
	for i := end - 1; i >= 0 && len(out) < limit; i-- {
		e := m.Audit[i]
		e.ID = strconv.Itoa(i + 1)
		out = append(out, e)
	}
	next := ""
	if len(out) == limit && end-limit > 0 {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
