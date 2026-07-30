package main

import (
	"os"

	"github.com/aitoys/paas/internal/observability"
	obcompose "github.com/aitoys/paas/internal/observability/compose"
	obsmemory "github.com/aitoys/paas/internal/observability/memory"
	obreal "github.com/aitoys/paas/internal/observability/real"
)

// buildObservabilityStore 按环境变量构造 observability.Repository：
// alert rules 始终 memory（含 seed）；metrics/logs/traces 按
// PAAS_PROM_URL / PAAS_LOKI_URL / PAAS_TEMPO_URL 非空则接真实后端，
// 否则保持 memory 惰性 mock（三支柱独立、可混用）。
// 未配任何 URL 时行为与现状完全一致。
func buildObservabilityStore() observability.Repository {
	rules := obsmemory.NewStore()
	metrics := observability.MetricsReader(rules)
	if u := os.Getenv("PAAS_PROM_URL"); u != "" {
		metrics = obreal.NewMetricsStore(u)
	}
	logs := observability.LogsReader(rules)
	if u := os.Getenv("PAAS_LOKI_URL"); u != "" {
		logs = obreal.NewLogsStore(u)
	}
	traces := observability.TracesReader(rules)
	if u := os.Getenv("PAAS_TEMPO_URL"); u != "" {
		traces = obreal.NewTracesStore(u)
	}
	return obcompose.New(rules, metrics, logs, traces)
}
