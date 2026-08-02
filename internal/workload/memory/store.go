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
	mu            sync.RWMutex
	workloads     map[string]workload.Workload
	imageRegistry string // seed 镜像 registry 前缀（内网部署拼 <registry>/library/...）
}

// StoreOpt 配置 Store。
type StoreOpt func(*Store)

// WithImageRegistry 设置 seed 镜像的 registry 前缀。
// 集群部署（PAAS_IMAGE_REGISTRY=hub.wang.dd:5000）时 seed 镜像拼内网地址让节点可拉；
// 空（本地 dev）用公开名 nginx:stable（内存模式不投影 K8s，镜像真假无影响）。
func WithImageRegistry(registry string) StoreOpt {
	return func(s *Store) { s.imageRegistry = registry }
}

// NewStore 创建仓储并 seed 示例工作负载（真实 nginx 业务服务镜像）。
func NewStore(opts ...StoreOpt) *Store {
	s := &Store{workloads: map[string]workload.Workload{}}
	for _, o := range opts {
		o(s)
	}
	for _, w := range seed(s.imageRegistry) {
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

// UpdateImage 更新工作负载镜像（display + digest）。供 devops.Release 编排调用。
// imageRef 为空时不覆盖已有 digest（兼容仅刷新 display 的场景）。
func (s *Store) UpdateImage(ctx context.Context, id, image, imageRef string) (workload.Workload, error) {
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
	w.Image = image
	if imageRef != "" {
		w.ImageRef = imageRef
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

// SeedWorkloads 返回平台预置示例工作负载（真实 nginx 业务服务镜像），供内存仓储自灌与
// PG 仓储迁移后 seed 复用同一真源。registry 非空拼内网地址（集群可拉），空用公开名（dev）。
func SeedWorkloads(registry string) []workload.Workload { return seed(registry) }

// nginxImage 按 registry 拼接 nginx 镜像地址（去 repo 前缀走 library/，与 engineImage 同款）。
func nginxImage(registry string) string {
	if registry == "" {
		return "nginx:stable"
	}
	return registry + "/library/nginx:stable"
}

// seed 生成跨两租户的示例工作负载（真实 nginx 镜像，初始占位态由 K8s 回填真实）。
// acme: cs-api/rec-svc(service) + batch-recall(job) + daily-report(cronjob)，三类齐全；
// globex: etl-nightly(cronjob)/etl-backfill(job)/agent-gw(service)，多租户隔离对照。
//
// Ready/Status 为初始占位：集群模式 StatusReader 从 K8s 回填真实值（覆盖 store 静态值），
// 内存模式（无 K8s）保持初始态（dev 演示，不撒谎说 running）。service 镜像 nginx listen 80，
// Port/ContainerPort=80 驱动 reconciler 建 Service + TCP readiness probe -> Endpoints ready。
// job/cronjob 用 `nginx -v`（打印版本退出，演示一次性/定时任务成功语义）。
func seed(registry string) []workload.Workload {
	t := time.Now()
	d := workload.LaneDefault
	nginx := nginxImage(registry)
	return []workload.Workload{
		{ID: "wl-cs-api", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeService,
			Name: "cs-api", Image: nginx, Replicas: 2, Ready: 0, Status: workload.StatusDeploying,
			Port: 80, ContainerPort: 80, CreatedAt: t},
		{ID: "wl-rec-svc", TenantID: "t-acme", AppID: "app-rec", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeService,
			Name: "rec-svc", Image: nginx, Replicas: 3, Ready: 0, Status: workload.StatusDeploying,
			Port: 80, ContainerPort: 80, CreatedAt: t},
		{ID: "wl-acme-recall", TenantID: "t-acme", AppID: "app-rec", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeJob,
			Name: "batch-recall", Image: nginx, Replicas: 1, Ready: 0, Status: workload.StatusPending,
			Command: "nginx -v", CreatedAt: t},
		{ID: "wl-acme-report", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", LaneID: d, Type: workload.TypeCronJob,
			Name: "daily-report", Image: nginx, Replicas: 0, Ready: 0, Status: workload.StatusRunning,
			Schedule: "0 8 * * *", Command: "nginx -v", CreatedAt: t},
		{ID: "wl-etl-nightly", TenantID: "t-globex", AppID: "app-etl", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeCronJob,
			Name: "etl-nightly", Image: nginx, Replicas: 0, Ready: 0, Status: workload.StatusRunning,
			Schedule: "0 2 * * *", Command: "nginx -v", CreatedAt: t},
		{ID: "wl-etl-backfill", TenantID: "t-globex", AppID: "app-etl", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeJob,
			Name: "etl-backfill", Image: nginx, Replicas: 1, Ready: 0, Status: workload.StatusPending,
			Command: "nginx -v", CreatedAt: t},
		{ID: "wl-agent-gw", TenantID: "t-globex", AppID: "app-agent", EnvID: "env-globex-prod", LaneID: d, Type: workload.TypeService,
			Name: "agent-gw", Image: nginx, Replicas: 2, Ready: 0, Status: workload.StatusDeploying,
			Port: 80, ContainerPort: 80, CreatedAt: t},
	}
}
