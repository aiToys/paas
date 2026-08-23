// cache.go 查询缓存 + singleflight（R8-I1）：多用户 10s 轮询会把同一 Prometheus/Loki/Jaeger
// 查询全量扇出（QPS 放大 N 倍）。两层削峰：
//   - singleflight：同 key 并发请求只发一次上游查询，其余等结果共享（零依赖手写，官方库
//     golang.org/x/sync/singleflight 语义等价，避免为一个小特性引依赖）。
//   - 短 TTL 缓存：查询结果缓存 ttl（默认 5s，远小于 10s 轮询周期），窗口内直接命中。
//
// 失败结果不缓存（防后端抖动时错误被钉住）；返回值为共享切片，调用方只读不改。
package real

import (
	"context"
	"sync"
	"time"
)

// queryCache 泛型 singleflight + TTL 缓存。
type queryCache[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*cacheEntry[T]
	flights map[string]*flight[T] // 进行中的查询（singleflight）
	now     func() time.Time      // 可注入时钟（测试）
}

type cacheEntry[T any] struct {
	val T
	exp time.Time
}

type flight[T any] struct {
	done chan struct{}
	val  T
	err  error
}

func newQueryCache[T any](ttl time.Duration) *queryCache[T] {
	return &queryCache[T]{ttl: ttl, entries: map[string]*cacheEntry[T]{}, flights: map[string]*flight[T]{}, now: time.Now}
}

// do 取缓存或发起查询（同 key 并发合并为一次 fn）。
func (c *queryCache[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	c.mu.Lock()
	// 1. 缓存命中（未过期直接返回）。
	if e, ok := c.entries[key]; ok && c.now().Before(e.exp) {
		c.mu.Unlock()
		return e.val, nil
	}
	// 2. singleflight：已有同 key 查询在途，挂载等结果。
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.val, f.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
	// 3. 发起查询（用脱离请求生命周期的 ctx——缓存结果供后续请求共享，不被首个请求取消连带）。
	f := &flight[T]{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	val, err := fn(qctx)
	cancel()
	// 惰性清理：条目超上限（防 key 空间无界增长）时全量扫除过期项。
	c.mu.Lock()
	delete(c.flights, key)
	if err == nil {
		c.entries[key] = &cacheEntry[T]{val: val, exp: c.now().Add(c.ttl)}
		if len(c.entries) > 512 {
			now := c.now()
			for k, e := range c.entries {
				if now.After(e.exp) {
					delete(c.entries, k)
				}
			}
		}
	}
	c.mu.Unlock()
	close(f.done)
	f.val, f.err = val, err
	return val, err
}
