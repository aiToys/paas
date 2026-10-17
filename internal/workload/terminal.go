// Package workload —— 容器终端会话管理（spec 2026-10-17 §6 终端）。
//
// 会话生命周期：POST terminal-sessions 受理（60s 握手有效期、单次消费）→
// WS /api/terminal-sessions/{id}/stream 升级（复核权限/Origin/UID）→ 数据面 exec 流桥接 →
// 退出/超时/撤权关闭。K8s 凭证不出平台进程；浏览器只拿到不透明会话 ID。
package workload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// 终端会话策略常量（会话本身在内存——重启丢失即断开，符合终端语义）。
const (
	TerminalHandshakeTTL = 60 * time.Second // 受理到 WS 升级的窗口
	TerminalIdleTTL      = 5 * time.Minute  // 空闲超时（无输入输出）
	TerminalMaxTTL       = 30 * time.Minute // 硬上限
	TerminalMaxPerUser   = 2                // 每用户并发会话上限
)

// ErrTerminalDisabled / ErrTerminalBusy / ErrTerminalStale 是 handler 映射 503/409/410 的语义错误。
var (
	ErrTerminalDisabled = errors.New("容器终端未启用（PAAS_TERMINAL_ENABLED）")
	ErrTerminalBusy     = errors.New("并发终端会话数已达上限")
	ErrTerminalStale    = errors.New("会话已过期或已被使用，请重新发起")
)

// TerminalSession 是一个已受理的终端会话（受理记录 + 建立后的执行流句柄）。
type TerminalSession struct {
	ID         string
	TenantID   string
	UserID     string // 归因（claims.sub）；审计与并发限制的键
	WorkloadID string
	PodName    string
	PodUID     string
	Container  string
	Shell      string
	Reason     string
	CreatedAt  time.Time
	ExpiresAt  time.Time // 握手窗口

	// 建立后填充（Stream 成功后）。
	Exec     ExecSession
	lastSeen time.Time // 空闲检测（读写任一活动都刷新）
	closed   bool
}

// touch 刷新活跃时间（空闲超时判定用）。
func (s *TerminalSession) touch() { s.lastSeen = time.Now() }

// TerminalManager 管理终端会话的受理、消费与回收（进程内单例）。
type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*TerminalSession
	// Streamer 建立 exec 流（cmd/core 注入 controller 实现；nil=未启用）。
	Streamer ExecStreamer
}

// NewTerminalManager 创建会话管理器。streamer nil 时 Create 返 ErrTerminalDisabled。
func NewTerminalManager(streamer ExecStreamer) *TerminalManager {
	return &TerminalManager{sessions: map[string]*TerminalSession{}, Streamer: streamer}
}

// Enabled 报告终端能力是否开启（装配层据 feature flag 决定是否注入 Streamer）。
func (m *TerminalManager) Enabled() bool { return m.Streamer != nil }

// Create 受理一个终端会话（未建立流）：生成不透明 ID + 60s 握手窗口。
// 并发限制：同 (tenant,user) 未关闭会话数 < TerminalMaxPerUser，否则 ErrTerminalBusy。
func (m *TerminalManager) Create(tenantID, userID, workloadID, podName, podUID, container, shell, reason string) (string, time.Time, error) {
	if m.Streamer == nil {
		return "", time.Time{}, ErrTerminalDisabled
	}
	if shell != "/bin/sh" && shell != "/bin/bash" {
		return "", time.Time{}, errors.New("仅支持 /bin/sh 或 /bin/bash")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gcLocked()
	live := 0
	for _, s := range m.sessions {
		if s.TenantID == tenantID && s.UserID == userID && !s.closed {
			live++
		}
	}
	if live >= TerminalMaxPerUser {
		return "", time.Time{}, ErrTerminalBusy
	}
	id, err := randomID()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	exp := now.Add(TerminalHandshakeTTL)
	m.sessions[id] = &TerminalSession{
		ID: id, TenantID: tenantID, UserID: userID, WorkloadID: workloadID,
		PodName: podName, PodUID: podUID, Container: container, Shell: shell, Reason: reason,
		CreatedAt: now, ExpiresAt: exp, lastSeen: now,
	}
	return id, exp, nil
}

// Consume 单次消费握手：取走会话（未过期）并建立 exec 流。
// 会话绑定（tenant/workload/Pod UID/容器）在受理时固化，此处按 ID 精确取回。
func (m *TerminalManager) Consume(ctx context.Context, id string) (*TerminalSession, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil, ErrTerminalStale
	}
	if time.Now().After(s.ExpiresAt) {
		delete(m.sessions, id)
		m.mu.Unlock()
		return nil, ErrTerminalStale
	}
	// 握手窗口内标记消费：延 long TTL 防下一并发请求重复消费。
	s.ExpiresAt = time.Now().Add(TerminalMaxTTL)
	m.mu.Unlock()

	exec, err := m.Streamer.Stream(ctx, s.WorkloadID, s.PodName, s.PodUID, s.Container, s.Shell)
	if err != nil {
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	if _, still := m.sessions[id]; !still {
		m.mu.Unlock()
		exec.Close() // 竞态：已被回收，立刻关流
		return nil, ErrTerminalStale
	}
	s.Exec = exec
	s.touch()
	m.mu.Unlock()
	return s, nil
}

// Get 返回会话（WS 消息循环内用；不消费）。
func (m *TerminalManager) Get(id string) (*TerminalSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Close 关闭会话并终止 exec 流（用户 DELETE /对端退出/空闲超时/撤权）。
func (m *TerminalManager) Close(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if ok && s.Exec != nil {
		s.Exec.Close()
	}
}

// IdleExpired 检查会话是否空闲超时（消息循环里周期调用；活跃时返 false）。
func (m *TerminalManager) IdleExpired(s *TerminalSession, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.closed {
		return true
	}
	return now.Sub(s.lastSeen) > TerminalIdleTTL || now.After(s.ExpiresAt)
}

// Touch 活动刷新（收到输入/有输出时调）。
func (m *TerminalManager) Touch(s *TerminalSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.touch()
}

// gcLocked 清理过期/已关会话（须持锁）。
func (m *TerminalManager) gcLocked() {
	now := time.Now()
	for id, s := range m.sessions {
		if s.closed || now.After(s.ExpiresAt) {
			if s.Exec != nil {
				s.Exec.Close()
			}
			delete(m.sessions, id)
		}
	}
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
