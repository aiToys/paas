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
	mu    sync.Mutex
	clock clock
	fails map[string]*failEntry // key = "ip:<ip>" 或 "user:<name>"
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clock: realClock{}, fails: map[string]*failEntry{}}
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

// clientIP 取 X-Forwarded-For 首段（ingress 注入），退化 RemoteAddr（去端口）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
