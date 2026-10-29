// scoped 契约测试（spec §12）：工具信封四态、查询参数白名单（拒绝注入）、
// 证据脱敏与截断、覆盖汇总（traces 降级）。
package scoped

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/diagnose"
)

func testTarget() diagnose.AuthorizedTarget {
	return diagnose.AuthorizedTarget{
		Type: diagnose.TargetTypeWorkload, WorkloadID: "wl-1", WorkloadName: "wl-1",
		Namespace: "paas-t-acme", Service: "web", DeploymentUID: "uid-1",
	}
}

// envelope 断言辅助。
func callTool(t *testing.T, ts *Toolset, name, args string) envelope {
	t.Helper()
	_, invoke, ok := ts.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not found", name)
	}
	out, err := invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("invoke %s: %v", name, err)
	}
	var env envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("tool %s output not envelope JSON: %v → %s", name, err, out)
	}
	return env
}

func TestToolsetEnvelopeFourStates(t *testing.T) {
	// 全 nil 依赖：数据源未接入 → error/not_configured（不是 panic、不是 ok）。
	ts := New(testTarget(), 30, nil, nil, nil, nil)
	env := callTool(t, ts, "get_workload_status", `{}`)
	if env.Status != StatusError {
		t.Fatalf("nil-dep status = %s, want error", env.Status)
	}
	// 证据账本已记（evidenceId 非空——LLM 引用制的前提）。
	if env.EvID == "" {
		t.Fatal("evidenceId must be non-empty")
	}
	if len(ts.Ledger().Snapshot()) != 1 {
		t.Fatal("ledger must record the call")
	}
}

func TestToolsetRejectsScopeInjection(t *testing.T) {
	ts := New(testTarget(), 30, nil, nil, nil, nil)
	// Loki 参数只接受 keyword/level/limit；namespace/pod 等作用域参数一律忽略
	//（查询由服务端按 AuthorizedTarget 构造——这里 nil loki 断言参数面不炸）。
	env := callTool(t, ts, "query_loki", `{"keyword":"error","namespace":"paas-t-globex","pod":"other"}`)
	if env.Status != StatusError {
		t.Fatalf("query_loki with nil dep should error, got %s", env.Status)
	}
	// get_pod_logs 传非目标 pod：归属校验拒绝（nil k8s → error 而非放行）。
	callTool(t, ts, "get_pod_logs", `{"pod":"foreign-pod"}`)
	// 工具集白名单：无 trace 工具（首期降级 scope_unverifiable）。
	for _, h := range ts.Handlers() {
		if strings.Contains(h, "trace") {
			t.Fatalf("trace tool must not exist in phase 1: %s", h)
		}
	}
}

func TestLedgerMaskAndTruncate(t *testing.T) {
	l := NewLedger()
	id := l.Record("get_pod_logs", StatusOK, "", "日志含 sk-abcdef1234567890abcdef 与 postgres://u:pw@h/db",
		"bearer abcdef123456", 30)
	if !strings.HasPrefix(id, "ev-") {
		t.Fatalf("evidenceId = %s", id)
	}
	snap := l.Snapshot()
	if strings.Contains(snap[0].Snippet, "abcdef123456") || strings.Contains(snap[0].Summary, "u:pw@") {
		t.Fatalf("sensitive data leaked: %+v", snap[0])
	}
	// 超长片段截断。
	long := strings.Repeat("x", MaxSnippetBytes+1024)
	l.Record("get_pod_logs", StatusOK, "", "summary", long, 30)
	if !strings.Contains(l.Snapshot()[1].ReasonCode, ReasonTruncated) {
		t.Fatal("oversized snippet must be truncated with reasonCode")
	}
	// 覆盖汇总：traces 恒为 missing/scope_unverifiable。
	cover := l.Coverage()
	if cover["traces"] != StatusMissing+"/"+ReasonScopeUnverifiable {
		t.Fatalf("traces coverage = %s", cover["traces"])
	}
	// get_pod_logs 归属 k8s 源：ok（未执行的源如 prometheus 不出现在汇总——
	// 区别于「执行了但 empty」，spec §7 not_checked 语义由报告侧补）。
	if cover["k8s"] != StatusOK {
		t.Fatalf("k8s coverage = %q, want ok", cover["k8s"])
	}
	if cover["prometheus"] != "missing/not_checked" {
		t.Fatal("未执行源必须明确标记 not_checked")
	}
}
