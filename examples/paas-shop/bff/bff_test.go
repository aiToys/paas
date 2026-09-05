package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventRingPushAndOrder(t *testing.T) {
	r := newEventRing(3)
	now := time.Now()
	for i := 0; i < 5; i++ {
		r.push(shopEvent{Type: "product.created", ProductID: i, Name: "p", At: now, ReceivedAt: now.Add(time.Duration(i) * time.Second)})
	}
	got := r.latest(10)
	if len(got) != 3 { // 环形覆写，只留最近 3 条
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].ProductID != 4 { // 最新在前
		t.Fatalf("want newest first, got %d", got[0].ProductID)
	}
}

func TestEventRingMultiOverwriteOrder(t *testing.T) {
	// 多圈覆写：8 条入容量 5 环，应返最近 5 条倒序 [7,6,5,4,3]。
	// （物理序为 [5,6,7,3,4]，若按物理序倒序会错返 [4,3,7,6,5]。）
	r := newEventRing(5)
	for i := 0; i < 8; i++ {
		r.push(shopEvent{Type: "product.created", ProductID: i})
	}
	got := r.latest(10)
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	want := []int{7, 6, 5, 4, 3}
	for i, w := range want {
		if got[i].ProductID != w {
			t.Fatalf("order[%d]: want %d, got %d (all: %+v)", i, w, got[i].ProductID, got)
		}
	}
}

func TestEventsEndpoint(t *testing.T) {
	r := newEventRing(10)
	now := time.Now()
	r.push(shopEvent{Type: "product.updated", ProductID: 1, Name: "k", At: now, ReceivedAt: now})
	srv := httptest.NewServer(buildMux(r))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	var evs []shopEvent
	if err := json.NewDecoder(resp.Body).Decode(&evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != "product.updated" {
		t.Fatalf("unexpected: %+v", evs)
	}
}

// TestStatsEndpoint /api/stats 返回 stats-consumer 累计结构（初始零值也 200）。
func TestStatsEndpoint(t *testing.T) {
	stats := newStatsAgg()
	ts := httptest.NewServer(buildMuxWithStats(newEventRing(10), stats))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/stats 应 200，got %d", resp.StatusCode)
	}
	var s map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"orders", "gmvCents", "byStatus"} {
		if _, ok := s[key]; !ok {
			t.Errorf("stats 应含 %s，got %v", key, s)
		}
	}
}

// TestStatsAggConsume order.created 事件累计订单数/GMV/状态分布。
func TestStatsAggConsume(t *testing.T) {
	a := newStatsAgg()
	a.consume([]byte(`{"type":"order.created","orderId":"ORD-1","amount":9900,"status":"pending"}`))
	a.consume([]byte(`{"type":"order.paid","orderId":"ORD-1","amount":9900,"status":"paid"}`))
	if a.snapshot().Orders != 1 {
		t.Fatalf("order.created 应 +1，got %d", a.snapshot().Orders)
	}
	if a.snapshot().GMVCents != 9900 {
		t.Fatalf("GMV 应 9900，got %d", a.snapshot().GMVCents)
	}
	if a.snapshot().ByStatus["paid"] != 1 {
		t.Fatalf("状态分布应 paid=1，got %v", a.snapshot().ByStatus)
	}
}
