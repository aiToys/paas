package gateway

import (
	"log"
	"sync"
)

// Meter 记录 Token 用量。本切片仅 log + 内存累计；Plan 2 接 PG/ClickHouse。
type Meter struct {
	mu    sync.Mutex
	total int
}

// Record 记录一次请求的 token 用量。
func (m *Meter) Record(tenantID, model string, tokens int) {
	m.mu.Lock()
	m.total += tokens
	m.mu.Unlock()
	log.Printf("[meter] tenant=%s model=%s tokens=%d", tenantID, model, tokens)
}

// Count 返回累计 token（测试用）。
func (m *Meter) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}
