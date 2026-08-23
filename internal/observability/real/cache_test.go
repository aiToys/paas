package real

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestQueryCacheTTL：TTL 内命中缓存不重查，过期后重查。
func TestQueryCacheTTL(t *testing.T) {
	now := time.Now()
	c := newQueryCache[int](5 * time.Second)
	c.now = func() time.Time { return now }
	var calls int32
	fn := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 42, nil
	}
	v, _ := c.do(context.Background(), "k", fn)
	v2, _ := c.do(context.Background(), "k", fn)
	if v != 42 || v2 != 42 || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("TTL 内应命中缓存 calls=1, got calls=%d", calls)
	}
	now = now.Add(6 * time.Second) // 过期
	v3, _ := c.do(context.Background(), "k", fn)
	if v3 != 42 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("过期后应重查 calls=2, got calls=%d", calls)
	}
}

// TestQueryCacheSingleflight：同 key 并发只执行一次 fn。
func TestQueryCacheSingleflight(t *testing.T) {
	c := newQueryCache[int](time.Minute)
	var calls int32
	fn := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond) // 拉长在途窗口，让并发请求挂载 flight
		return 7, nil
	}
	const n = 10
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = c.do(context.Background(), "k", fn)
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("并发应合并为 1 次查询, got %d", calls)
	}
	for _, r := range results {
		if r != 7 {
			t.Fatalf("结果应 7, got %d", r)
		}
	}
}

// TestQueryCacheErrorNotCached：失败结果不缓存（下次重试）。
func TestQueryCacheErrorNotCached(t *testing.T) {
	c := newQueryCache[int](time.Minute)
	var calls int32
	fn := func(ctx context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 0, errFake
	}
	_, _ = c.do(context.Background(), "k", fn)
	_, _ = c.do(context.Background(), "k", fn)
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("失败不应缓存（两次都执行）, got %d", calls)
	}
}

var errFake = fakeErr{}

type fakeErr struct{}

func (fakeErr) Error() string { return "fake" }
