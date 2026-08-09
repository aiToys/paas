package pipeline

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// memoryStore 三仓储的进程内实现。供 cmd/core 在 PAAS_DB_URL 为空时装配。
// 所有方法强制按 ctx 租户过滤；跨租户访问返 NotFound 不泄漏存在性。
type memoryStore struct {
	mu        sync.RWMutex
	pipes     map[string]Pipeline
	runs      map[string]PipelineRun
	templates map[string]PipelineTemplate
}

// NewMemoryStore 创建空 store（不 seed 演示数据，去假数据门控由 cmd/core 控制）。
func NewMemoryStore() *memoryStore {
	return &memoryStore{
		pipes:     map[string]Pipeline{},
		runs:      map[string]PipelineRun{},
		templates: map[string]PipelineTemplate{},
	}
}

// tenantOrErr 从 ctx 取租户，缺失返 ErrNoTenant。
func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	return tid, nil
}

// ---------- Repository: Pipeline ----------

func (s *memoryStore) ListPipelines(ctx context.Context, appID string) ([]Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Pipeline, 0)
	for _, p := range s.pipes {
		if p.TenantID != tid {
			continue
		}
		if appID != "" && p.AppID != appID {
			continue
		}
		out = append(out, clonePipeline(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *memoryStore) GetPipeline(ctx context.Context, id string) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pipes[id]
	if !ok || p.TenantID != tid {
		return Pipeline{}, ErrPipelineNotFound
	}
	return clonePipeline(p), nil
}

// GetPipelineAny 跨租户按 ID 查（webhook 触发用，token 鉴权在 handler 层）。
func (s *memoryStore) GetPipelineAny(ctx context.Context, id string) (Pipeline, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pipes[id]
	if !ok {
		return Pipeline{}, ErrPipelineNotFound
	}
	return clonePipeline(p), nil
}

func (s *memoryStore) CreatePipeline(ctx context.Context, p Pipeline) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	// 以 ctx 为准，忽略请求体的 tenantId（防越权写）
	p.TenantID = tid
	if err := p.Validate(); err != nil {
		return Pipeline{}, err
	}
	if p.ID == "" {
		p.ID = newID("pipe")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同 (tenant,app,name) 唯一
	for _, ex := range s.pipes {
		if ex.TenantID == tid && ex.AppID == p.AppID && ex.Name == p.Name {
			return Pipeline{}, ErrPipelineExists
		}
	}
	if _, exists := s.pipes[p.ID]; exists {
		return Pipeline{}, ErrPipelineExists
	}
	s.pipes[p.ID] = p
	return clonePipeline(p), nil
}

func (s *memoryStore) UpdatePipeline(ctx context.Context, p Pipeline) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	if err := p.Validate(); err != nil {
		return Pipeline{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.pipes[p.ID]
	if !ok || ex.TenantID != tid {
		return Pipeline{}, ErrPipelineNotFound
	}
	// 同 (tenant,app,name) 唯一——改名冲突检查（排除自身）
	for _, other := range s.pipes {
		if other.ID == p.ID {
			continue
		}
		if other.TenantID == tid && other.AppID == p.AppID && other.Name == p.Name {
			return Pipeline{}, ErrPipelineExists
		}
	}
	p.TenantID = tid // 锁定租户归属，忽略请求体
	p.CreatedAt = ex.CreatedAt
	s.pipes[p.ID] = p
	return clonePipeline(p), nil
}

// DeletePipeline 级联清该 pipeline 的 runs（参照 devops DeleteService 级联模式）。
func (s *memoryStore) DeletePipeline(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pipes[id]
	if !ok || p.TenantID != tid {
		return ErrPipelineNotFound
	}
	delete(s.pipes, id)
	// 级联清 runs
	for rid, r := range s.runs {
		if r.TenantID == tid && r.PipelineID == id {
			delete(s.runs, rid)
		}
	}
	return nil
}

// ---------- RunRepository: PipelineRun ----------

func (s *memoryStore) ListRuns(ctx context.Context, appID, pipelineID, status string) ([]PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PipelineRun, 0)
	for _, r := range s.runs {
		if r.TenantID != tid {
			continue
		}
		if appID != "" && r.AppID != appID {
			continue
		}
		if pipelineID != "" && r.PipelineID != pipelineID {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		out = append(out, cloneRun(r))
	}
	// 倒序（最新优先）
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *memoryStore) GetRun(ctx context.Context, id string) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok || r.TenantID != tid {
		return PipelineRun{}, ErrRunNotFound
	}
	return cloneRun(r), nil
}

// CreateRun 单实例串行：先校验无 running/paused 运行，否则返 ErrActiveRunExists。
func (s *memoryStore) CreateRun(ctx context.Context, r PipelineRun) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	r.TenantID = tid
	if r.ID == "" {
		r.ID = newID("run")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 校验 pipeline 存在 + 归属
	p, ok := s.pipes[r.PipelineID]
	if !ok || p.TenantID != tid {
		return PipelineRun{}, ErrPipelineNotFound
	}
	// 单实例串行：已有 running/paused 拒绝
	for _, ex := range s.runs {
		if ex.TenantID == tid && ex.PipelineID == r.PipelineID &&
			(ex.Status == RunRunning || ex.Status == RunPaused) {
			return PipelineRun{}, ErrActiveRunExists
		}
	}
	if _, exists := s.runs[r.ID]; exists {
		return PipelineRun{}, ErrRunExists
	}
	s.runs[r.ID] = r
	return cloneRun(r), nil
}

// UpdateRun 全量写回（engine 推进时调）：stageRuns + currentStage + status + version + finishedAt。
func (s *memoryStore) UpdateRun(ctx context.Context, r PipelineRun) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.runs[r.ID]
	if !ok || ex.TenantID != tid {
		return PipelineRun{}, ErrRunNotFound
	}
	r.TenantID = tid
	r.CreatedAt = ex.CreatedAt
	s.runs[r.ID] = r
	return cloneRun(r), nil
}

func (s *memoryStore) HasActiveRun(ctx context.Context, pipelineID string) (bool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runs {
		if r.TenantID == tid && r.PipelineID == pipelineID &&
			(r.Status == RunRunning || r.Status == RunPaused) {
			return true, nil
		}
	}
	return false, nil
}

// ---------- TemplateRepository: PipelineTemplate ----------

// ListTemplates 返平台预置（tenant_id=""）+ 本租户自定义。
func (s *memoryStore) ListTemplates(ctx context.Context) ([]PipelineTemplate, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PipelineTemplate, 0)
	for _, t := range s.templates {
		if t.TenantID == "" || t.TenantID == tid {
			out = append(out, cloneTemplate(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID == "" // 平台预置在前
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *memoryStore) GetTemplate(ctx context.Context, id string) (PipelineTemplate, error) {
	// 无租户 ctx（平台级 seed 升级路径）仅可访问平台预置（TenantID=""）；有租户 ctx 校验本租户。
	tid, _ := tenant.TenantFrom(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.templates[id]
	if !ok {
		return PipelineTemplate{}, ErrPipelineNotFound
	}
	// 平台预置（tenant_id="")或本租户自定义可见；跨租户 not found 不泄漏
	if t.TenantID != "" && t.TenantID != tid {
		return PipelineTemplate{}, ErrPipelineNotFound
	}
	return cloneTemplate(t), nil
}

// CreateTemplate 模板特判：平台预置（tenant_id="")不受 tenantOrErr 拦截，
// 但仍需 ctx 持有租户（调用方为平台 admin）。
func (s *memoryStore) CreateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error) {
	// 平台预置（t.TenantID=""）不要求 ctx 租户（admin 平台级 seed）；租户自定义需 ctx 租户。
	tid, _ := tenant.TenantFrom(ctx)
	// 模板特判：tenant_id=""（平台预置）或 =本租户（自定义）允许；
	// 跨租户注入（!="" && !=tid）一律拒并锁定为本租户（防越权写）。
	if t.TenantID != "" && t.TenantID != tid {
		t.TenantID = tid
	}
	if t.ID == "" {
		t.ID = newID("tpl")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同 (tenant, name) 唯一：平台预置间不重名、租户自定义间不重名
	for _, ex := range s.templates {
		if ex.TenantID == t.TenantID && ex.Name == t.Name {
			return PipelineTemplate{}, ErrTemplateExists
		}
	}
	if _, exists := s.templates[t.ID]; exists {
		return PipelineTemplate{}, ErrTemplateExists
	}
	s.templates[t.ID] = t
	return cloneTemplate(t), nil
}

// UpdateTemplate 更新自定义模板。builtin 模板拒（防误改致新应用默认 binding 漂移）。
// 平台预置（t.TenantID=""）允许 admin 改；租户自定义需 ctx 租户匹配。
func (s *memoryStore) UpdateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error) {
	tid, _ := tenant.TenantFrom(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.templates[t.ID]
	if !ok {
		return PipelineTemplate{}, ErrPipelineNotFound
	}
	if ex.Builtin {
		return PipelineTemplate{}, ErrTemplateBuiltin
	}
	// 跨租户改他人自定义模板拒（ex.TenantID 非 "" 且 != ctx 租户）
	if ex.TenantID != "" && ex.TenantID != tid {
		return PipelineTemplate{}, ErrPipelineNotFound
	}
	// 同 (tenant, name) 唯一：改名撞同名拒
	if t.Name != ex.Name {
		for _, o := range s.templates {
			if o.ID != t.ID && o.TenantID == ex.TenantID && o.Name == t.Name {
				return PipelineTemplate{}, ErrTemplateExists
			}
		}
	}
	// 保留不可变字段（ID/TenantID/Builtin/CreatedAt），更新可变字段
	ex.Name = t.Name
	ex.Kind = t.Kind
	ex.Description = t.Description
	ex.Stages = cloneStages(t.Stages)
	ex.Params = cloneParamDefs(t.Params)
	s.templates[t.ID] = ex
	return cloneTemplate(ex), nil
}

// DeleteTemplate 删除自定义模板。builtin 模板拒。
func (s *memoryStore) DeleteTemplate(ctx context.Context, id string) error {
	tid, _ := tenant.TenantFrom(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.templates[id]
	if !ok {
		return ErrPipelineNotFound
	}
	if ex.Builtin {
		return ErrTemplateBuiltin
	}
	if ex.TenantID != "" && ex.TenantID != tid {
		return ErrPipelineNotFound
	}
	delete(s.templates, id)
	return nil
}

// ReplaceBuiltinTemplate 平台级 seed 专用：覆盖 builtin 模板的 stages/name/description/version。
// 绕过 UpdateTemplate 的 builtin 拒改保护（builtin 升级走代码发版，非 admin UI）。
// 仅 builtin=true 生效；不存在或非 builtin 返 ErrPipelineNotFound。
func (s *memoryStore) ReplaceBuiltinTemplate(ctx context.Context, t PipelineTemplate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.templates[t.ID]
	if !ok || !ex.Builtin {
		return ErrPipelineNotFound
	}
	// 保留 ID/TenantID/Builtin/Kind/CreatedAt，覆盖可变字段
	ex.Name = t.Name
	ex.Description = t.Description
	ex.Stages = cloneStages(t.Stages)
	ex.Params = cloneParamDefs(t.Params)
	ex.Version = t.Version
	s.templates[t.ID] = ex
	return nil
}

// ---------- 深拷贝（防 race：engine 读改与 list/get 读并发时切片/map 撕裂） ----------

func clonePipeline(p Pipeline) Pipeline {
	cp := p
	cp.ParamOverrides = cloneStringAnyMap(p.ParamOverrides)
	cp.Trigger.Events = cloneStrings(p.Trigger.Events)
	return cp
}

func cloneRun(r PipelineRun) PipelineRun {
	cp := r
	cp.StageRuns = cloneStageRuns(r.StageRuns)
	return cp
}

func cloneTemplate(t PipelineTemplate) PipelineTemplate {
	cp := t
	cp.Stages = cloneStages(t.Stages)
	cp.Params = cloneParamDefs(t.Params)
	return cp
}

func cloneStages(in []StageDef) []StageDef {
	if in == nil {
		return nil
	}
	out := make([]StageDef, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Params = cloneStringAnyMap(s.Params)
	}
	return out
}

func cloneParamDefs(in []ParamDef) []ParamDef {
	if in == nil {
		return nil
	}
	out := make([]ParamDef, len(in))
	copy(out, in)
	return out
}

func cloneStageRuns(in []StageRun) []StageRun {
	if in == nil {
		return nil
	}
	out := make([]StageRun, len(in))
	for i, r := range in {
		out[i] = r
		out[i].Input = cloneStringAnyMap(r.Input)
		out[i].Output = cloneStringAnyMap(r.Output)
	}
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
