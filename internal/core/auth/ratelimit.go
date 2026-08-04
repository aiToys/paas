package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginMaxFails = 5                // 窗口内失败次数上限
	loginWindow   = 5 * time.Minute  // 失败计数窗口
	loginLockout  = 15 * time.Minute // 超限锁定时长
)

// clock 抽象便于测试；生产用真实 time。
type clock interface{ now() time.Time }
type realClock struct{}

func (realClock) now() time.Time { return time.Now() }

type failEntry struct {
	count       int
	firstAt     time.Time
	lockedUntil time.Time
}

// loginLimiter per-IP + per-username 内存令牌桶。
// 失败 loginMaxFails 次/loginWindow -> 锁 loginLockout；成功清零。
// 单 core 实例够用；多副本上 Redis 延后。
type loginLimiter struct {
	mu        sync.Mutex
	clock     clock
	fails     map[string]*failEntry // key = "ip:<ip>" 或 "user:<name>"
	lastSweep time.Time             // 上次惰性清理时间
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clock: realClock{}, fails: map[string]*failEntry{}}
}

// sweep 清理完全过期的条目（lockedUntil 已过 且 窗口外），防 fails map 无限增长。
// 惰性触发：每次写操作（recordFailure/recordSuccess）若距上次清理超过 loginWindow 即执行。
// 持锁内调用，无需再加锁。
func (l *loginLimiter) sweep(now time.Time) {
	for k, e := range l.fails {
		// 条目完全过期：锁定已解除 且 首次失败已超出计数窗口（不会再被复用）。
		if !now.Before(e.lockedUntil) && now.Sub(e.firstAt) > loginWindow {
			delete(l.fails, k)
		}
	}
}

// allow 检查是否允许尝试（未锁）。不消费计数。
func (l *loginLimiter) allow(ip, username string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.now()
	for _, k := range []string{ipKey(ip), userKey(username)} {
		if e, ok := l.fails[k]; ok && now.Before(e.lockedUntil) {
			return false, e.lockedUntil.Sub(now)
		}
	}
	return true, 0
}

// recordFailure 失败计数 + 触发锁定。窗口外重置。
func (l *loginLimiter) recordFailure(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.now()
	if now.Sub(l.lastSweep) > loginWindow {
		l.sweep(now)
		l.lastSweep = now
	}
	for _, k := range []string{ipKey(ip), userKey(username)} {
		e := l.fails[k]
		if e == nil || now.Sub(e.firstAt) > loginWindow {
			e = &failEntry{firstAt: now}
			l.fails[k] = e
		}
		e.count++
		if e.count >= loginMaxFails {
			e.lockedUntil = now.Add(loginLockout)
		}
	}
}

// recordSuccess 成功清零。
func (l *loginLimiter) recordSuccess(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ipKey(ip))
	delete(l.fails, userKey(username))
}

func ipKey(ip string) string  { return "ip:" + ip }
func userKey(u string) string { return "user:" + u }

// clientIP 提取真实客户端 IP，用于登录限流的 per-IP 计数。
//
// 信任顺序（依赖 ingress 清洗客户端伪造头，hermes/nginx ingress 默认覆盖）：
//  1. X-Real-IP：ingress 注入的真实 client IP（覆盖客户端伪造值），最可信
//  2. X-Forwarded-For 最右段：ingress 追加在末尾，首段可能是客户端伪造——取最右避免被伪造绕过限流
//  3. RemoteAddr：直连退化（去端口）
//
// 安全：原实现取 XFF 首段，攻击者每请求换一个随机 XFF 首段即可让 per-IP 限流永不触发。
func clientIP(r *http.Request) string {
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 取最右段（ingress 追加的真实 IP）；首段可能被客户端伪造。
		parts := strings.Split(xff, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
