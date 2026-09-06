// Package safego 提供后台 goroutine 的 panic 防护（recover + 结构化日志 + 栈）。
//
// HTTP 路径有 recovery 中间件兜底；后台 goroutine 没有——一次 panic 即整进程崩溃
// （LaneGC 曾因 Now 为 nil 在生产 SIGSEGV，根因修了但同类防线缺失，R5 审计 I-3 立项）。
// 需要补偿写库的场景（workflow/pipeline 引擎 panic 后标 run failed）不适用本包，
// 各自内联 recover。
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Loop 给周期循环的单轮迭代包 panic 防护：panic 记日志后吞掉，循环下一轮继续
// （单轮故障不应终止整个后台循环，也不应杀进程）。
func Loop(name string, fn func()) {
	defer recoverLog(name)
	fn()
}

// Go 启动带 panic 防护的一次性 goroutine：panic 记日志后吞掉。
func Go(name string, fn func()) {
	go func() {
		defer recoverLog(name)
		fn()
	}()
}

func recoverLog(name string) {
	if rec := recover(); rec != nil {
		//nolint:gosec // 栈信息入日志供排障，panic 值来自仓内代码非外部注入
		slog.Error("后台任务 panic 已恢复", "name", name, "panic", rec, "stack", string(debug.Stack()))
	}
}
