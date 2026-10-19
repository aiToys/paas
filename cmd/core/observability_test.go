package main

import (
	"context"
	"testing"

	obspg "github.com/aitoys/paas/internal/observability/pg"
)

// TestBuildObservabilityStoreTypedNilPGStore 锁 typed-nil 装箱防御（2026-10-18）：
// 内存路径 stores.AlertEvents 是 *obspg.Store(nil)。若 buildObservabilityStore 参数是接口类型，
// typed nil 装箱成非 nil 接口 → engine.Restore 放行 → LoadStates 解引用 nil db panic 卡死启动
// （migration 0042 引入；集群 PG 路径不受影响故长期未暴露，本地内存模式必现）。
// 参数收窄为具体类型 + 函数内装箱前 nil 判断后，typed nil 必须被安全降级为纯内存状态机。
func TestBuildObservabilityStoreTypedNilPGStore(t *testing.T) {
	var pgStore *obspg.Store // typed nil（内存路径形态）
	_, engine := buildObservabilityStore(nil, nil, nil, pgStore)
	if engine == nil {
		t.Fatal("engine 不应为 nil")
	}
	// 修复前此处 panic：sigpanic in (*Store).LoadStates
	engine.Restore(context.Background())
}
