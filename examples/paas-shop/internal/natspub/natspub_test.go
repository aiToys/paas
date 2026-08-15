package natspub

import "testing"

func TestPublisherDegradedWhenURLEmpty(t *testing.T) {
	p := Connect("") // NATS_URL 空 → 降级 stub，不 panic 不退出
	if p.Connected() {
		t.Fatal("空 url 不应 Connected")
	}
	// 降级 stub 的 Publish 静默丢弃，返 nil（调用方无感，不阻断业务）
	if err := p.Publish("shop-events", []byte(`{"type":"product.changed"}`)); err != nil {
		t.Fatalf("降级 Publish 应返 nil，got %v", err)
	}
	p.Close() // no-op，不应 panic
}

func TestPublisherDegradedWhenUnreachable(t *testing.T) {
	// 指向不可达地址 → 连接失败也降级，不 panic
	p := Connect("nats://127.0.0.1:1")
	if p.Connected() {
		t.Fatal("不可达地址不应 Connected")
	}
	if err := p.Publish("shop-events", []byte(`x`)); err != nil {
		t.Fatalf("降级 Publish 应返 nil，got %v", err)
	}
	p.Close()
}
