// Package memory 提供 workload.Repository 的内存实现。
// 初始化时 seed 一批工作负载（跨两租户），便于控制台演示；未来替换为 K8s controller-runtime。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 是 workload.Repository 的内存实现。
type Store struct {
	mu        sync.RWMutex
	workloads map[string]workload.Workload
}

// NewStore 创建仓储并 seed 示例工作负载。
func NewStore() *Store {
	s := &Store{workloads: map[string]workload.Workload{}}
	for _, w := range seed() {
		s.workloads[w.ID] = w
	}
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

func (s *Store) List(ctx context.Context, envID, appID, wtype string) ([]workload.Workload, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]workload.Workload, 0)
	for _, w := range s.workloads {
		if w.TenantID != tid {
			continue
		}
		if envID != "" && w.EnvID != envID {
			continue
		}
		if appID != "" && w.AppID != appID {
			continue
		}
		if wtype != "" && w.Type != wtype {
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (workload.Workload, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, hit := s.workloads[id]
	if !hit || w.TenantID != tid {
		return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
	}
	return w, nil
}

func (s *Store) Create(ctx context.Context, w workload.Workload) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.ID == "" {
		return fmt.Errorf("工作负载 ID 不能为空")
	}
	if _, exists := s.workloads[w.ID]; exists {
		return fmt.Errorf("工作负载已存在: %s", w.ID)
	}
	w.TenantID = tid // 以 ctx 为准
	if w.LaneID == "" {
		w.LaneID = workload.LaneDefault // 默认基线
	}
	s.workloads[w.ID] = w
	return nil
}

func (s *Store) Update(ctx context.Context, id string, replicas int, status string) (workload.Workload, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, hit := s.workloads[id]
	if !hit || w.TenantID != tid {
		return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
	}
	w.Replicas = replicas
	if status != "" {
		w.Status = status
		// 就绪副本跟随期望：running/deploying 时 ready 取 replicas（mock 语义）
		if status == workload.StatusRunning {
			w.Ready = replicas
		}
	}
	s.workloads[id] = w
	return w, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, hit := s.workloads[id]
	if !hit || w.TenantID != tid {
		return fmt.Errorf("工作负载不存在: %s", id)
	}
	delete(s.workloads, id)
	return nil
}

// seed 生成跨两租户的示例工作负载，挂到环境（LaneID 均为 default 基线）。
// acme: cs-api/rec-svc -> env-acme-test；globex: etl/agent -> env-globex-prod。
func seed() []workload.Workload {
	t := time.Now()
	d := workload.LaneDefault
	return []workload.Workload{
		{ID: "wl-cs-api", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeService,
			Name: "cs-api", Image: "paas/qwen-cs:7b", Replicas: 2, Ready: 2, Status: workload.StatusRunning, CreatedAt: t},
		{ID: "wl-rec-svc", TenantID: "t-acme", AppID: "app-rec", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeService,
			Name: "rec-svc", Image: "paas/rec:latest", Replicas: 4, Ready: 3, Status: workload.StatusDeploying, CreatedAt: t},
		{ID: "wl-etl-nightly", TenantID: "t-globex", AppID: "app-etl", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeCronJob,
			Name: "etl-nightly", Image: "paas/etl:1.2", Replicas: 0, Ready: 0, Status: workload.StatusSucceeded,
			Schedule: "0 2 * * *", CreatedAt: t},
		{ID: "wl-etl-backfill", TenantID: "t-globex", AppID: "app-etl", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeJob,
			Name: "etl-backfill", Image: "paas/etl:1.2", Replicas: 2, Ready: 1, Status: workload.StatusRunning, CreatedAt: t},
		{ID: "wl-agent-gw", TenantID: "t-globex", AppID: "app-agent", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeService,
			Name: "agent-gw", Image: "paas/agent:0.9", Replicas: 2, Ready: 2, Status: workload.StatusRunning, CreatedAt: t},
	}
}
