package main

import (
	"context"
	"os"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/observability"
	obcompose "github.com/aitoys/paas/internal/observability/compose"
	obsmemory "github.com/aitoys/paas/internal/observability/memory"
	obreal "github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
)

// buildObservabilityStore 按环境变量构造 observability.Repository：
// alert rules 始终 memory（含 seed）；metrics/logs/traces 按
// PAAS_PROM_URL / PAAS_LOKI_URL / PAAS_JAEGER_URL 非空则接真实后端，
// 否则保持 memory 惰性 mock（三支柱独立、可混用）。
// 未配任何 URL 时行为与现状完全一致。
//
// trace 后端用 Jaeger all-in-one（天生单体，~256Mi 稳定），core 推送端走 OTel OTLP/HTTP
// （PAAS_OTEL_ENDPOINT 指向 Jaeger 4318），后端可插拔（Tempo/Jaeger/Zipkin 均接 OTLP）。
//
// lister 桥接 workload.Repository，供应用级 metrics/logs 按工作负载 pod 名正则查询
// （cAdvisor/Loki 不带 paas 自定义 label，靠 app→工作负载 ID 解析 pod 集合）。nil 时应用级查询降级返空。
// entities 桥接 application/dataservice Repository，供全局（targetType 空）指标聚合
// （可观测大屏「全部」视图健康矩阵）。nil 时全局查询降级返空。
func buildObservabilityStore(lister observability.AppWorkloadLister, entities observability.TenantEntityLister) observability.Repository {
	rules := obsmemory.NewStore()
	metrics := observability.MetricsReader(rules)
	if u := os.Getenv("PAAS_PROM_URL"); u != "" {
		metrics = obreal.NewMetricsStore(u, lister, entities)
	}
	logs := observability.LogsReader(rules)
	if u := os.Getenv("PAAS_LOKI_URL"); u != "" {
		logs = obreal.NewLogsStore(u, lister)
	}
	traces := observability.TracesReader(rules)
	if u := os.Getenv("PAAS_JAEGER_URL"); u != "" {
		traces = obreal.NewTracesStore(u, lister, entities)
	}
	return obcompose.New(rules, metrics, logs, traces)
}

// workloadLister 桥接 workload.Repository → observability.AppWorkloadLister。
// 应用级可观测按 app→工作负载 ID 列表解析 pod 名正则（Deployment 名=工作负载 ID，Pod=<id>-<hash>-<hash>）。
type workloadLister struct{ repo workload.Repository }

func (l workloadLister) AppWorkloadIDs(ctx context.Context, appID string) ([]string, error) {
	wls, err := l.repo.List(ctx, "", appID, "", "", "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(wls))
	for _, w := range wls {
		if w.ID != "" {
			ids = append(ids, w.ID)
		}
	}
	return ids, nil
}

// AppWorkloadNames 返回应用下全部 service 工作负载名（Deployment/Service 名 = OTel service.name）。
// 应用级 trace 查询按 service.name 匹配工作负载名定位该应用的 span。仅 service 类型（job/cronjob
// 无常驻 HTTP server，不产生入站请求 trace）；同名去重（多环境基线可能同名）。
func (l workloadLister) AppWorkloadNames(ctx context.Context, appID string) ([]string, error) {
	wls, err := l.repo.List(ctx, "", appID, "", "", "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wls))
	seen := make(map[string]struct{}, len(wls))
	for _, w := range wls {
		if w.Type != "service" || w.Name == "" {
			continue
		}
		if _, dup := seen[w.Name]; dup {
			continue
		}
		seen[w.Name] = struct{}{}
		names = append(names, w.Name)
	}
	return names, nil
}

// tenantEntityLister 桥接 application/dataservice Repository → observability.TenantEntityLister。
// 全局指标聚合（可观测大屏「全部」视图）需列出租户全部实体 ID，逐实体查 Prometheus 拼装。
type tenantEntityLister struct {
	apps application.Repository
	ds   dataservice.Repository
	wls  workload.Repository
}

func (l tenantEntityLister) TenantAppIDs(ctx context.Context) ([]string, error) {
	list, err := l.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, a := range list {
		if a.ID != "" {
			ids = append(ids, a.ID)
		}
	}
	return ids, nil
}

func (l tenantEntityLister) TenantDataServiceIDs(ctx context.Context) ([]string, error) {
	list, err := l.ds.List(ctx, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, d := range list {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// TenantServiceNames 列出租户内全部 service 工作负载名（跨应用去重）。
// trace 租户隔离的可见服务白名单：平台级 ListTraces 与 GetTrace 归属校验共用。
func (l tenantEntityLister) TenantServiceNames(ctx context.Context) ([]string, error) {
	list, err := l.apps.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, a := range list {
		wls, err := l.wls.List(ctx, "", a.ID, "", "", "")
		if err != nil {
			continue // 单应用失败降级跳过（与 TenantAppIDs 容错语义一致）
		}
		for _, w := range wls {
			if w.Type != "service" || w.Name == "" {
				continue
			}
			if _, dup := seen[w.Name]; dup {
				continue
			}
			seen[w.Name] = struct{}{}
			names = append(names, w.Name)
		}
	}
	return names, nil
}
