// Package memory workflow 内存存储（与 skill/memory 同款模式：锁 + 深拷贝隔离）。
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/workflow"
	"github.com/aitoys/paas/pkg/tenant"
)

type Store struct {
	mu   sync.RWMutex
	defs map[string]workflow.WorkflowDef
	runs map[string]workflow.WorkflowRun
}

func NewStore() *Store {
	return &Store{
		defs: make(map[string]workflow.WorkflowDef),
		runs: make(map[string]workflow.WorkflowRun),
	}
}

const maxRunsPerTenant = 500

func randID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%x", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "", workflow.ErrWorkflowNotFound
	}
	return tid, nil
}

func cloneDef(d workflow.WorkflowDef) workflow.WorkflowDef {
	out := d
	out.Nodes = append([]workflow.NodeDef(nil), d.Nodes...)
	for i := range out.Nodes {
		out.Nodes[i].Branches = append([]workflow.Branch(nil), out.Nodes[i].Branches...)
		if out.Nodes[i].Config.Args != nil {
			out.Nodes[i].Config.Args = cloneMap(out.Nodes[i].Config.Args)
		}
	}
	return out
}

func cloneRun(r workflow.WorkflowRun) workflow.WorkflowRun {
	out := r
	out.Inputs = cloneMap(r.Inputs)
	out.NodeRuns = append([]workflow.NodeRun(nil), r.NodeRuns...)
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) List(ctx context.Context) ([]workflow.WorkflowDef, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]workflow.WorkflowDef, 0)
	for _, d := range s.defs {
		if d.TenantID == tid {
			out = append(out, cloneDef(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (workflow.WorkflowDef, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.defs[id]
	if !ok || d.TenantID != tid {
		return workflow.WorkflowDef{}, workflow.ErrWorkflowNotFound
	}
	return cloneDef(d), nil
}

func (s *Store) Create(ctx context.Context, in workflow.WorkflowDef) (workflow.WorkflowDef, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.defs {
		if d.TenantID == tid && d.Name == in.Name {
			return workflow.WorkflowDef{}, workflow.ErrWorkflowExists
		}
	}
	now := time.Now()
	in.ID = randID("wf-")
	in.TenantID = tid
	in.CreatedAt, in.UpdatedAt = now, now
	s.defs[in.ID] = cloneDef(in)
	return cloneDef(in), nil
}

func (s *Store) Update(ctx context.Context, in workflow.WorkflowDef) (workflow.WorkflowDef, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowDef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[in.ID]
	if !ok || d.TenantID != tid {
		return workflow.WorkflowDef{}, workflow.ErrWorkflowNotFound
	}
	for _, o := range s.defs {
		if o.TenantID == tid && o.Name == in.Name && o.ID != in.ID {
			return workflow.WorkflowDef{}, workflow.ErrWorkflowExists
		}
	}
	in.TenantID = tid
	in.CreatedAt = d.CreatedAt
	in.UpdatedAt = time.Now()
	s.defs[in.ID] = cloneDef(in)
	return cloneDef(in), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[id]
	if !ok || d.TenantID != tid {
		return workflow.ErrWorkflowNotFound
	}
	delete(s.defs, id)
	// 级联清运行历史（定义删除后运行无展示主体）
	for rid, r := range s.runs {
		if r.WorkflowID == id {
			delete(s.runs, rid)
		}
	}
	return nil
}

func (s *Store) WorkflowsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.defs), nil
}

func (s *Store) CreateRun(ctx context.Context, in workflow.WorkflowRun) (workflow.WorkflowRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in.ID = randID("wfr-")
	in.TenantID = tid
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	s.runs[in.ID] = cloneRun(in)
	s.sweepRunsLocked(tid)
	return cloneRun(in), nil
}

// sweepRunsLocked 租户运行历史环形清理（最老先删，防滥用增长）。
func (s *Store) sweepRunsLocked(tid string) {
	var ids []string
	for id, r := range s.runs {
		if r.TenantID == tid {
			ids = append(ids, id)
		}
	}
	if len(ids) <= maxRunsPerTenant {
		return
	}
	sort.Slice(ids, func(i, j int) bool { return s.runs[ids[i]].CreatedAt.Before(s.runs[ids[j]].CreatedAt) })
	for _, id := range ids[:len(ids)-maxRunsPerTenant] {
		delete(s.runs, id)
	}
}

func (s *Store) GetRun(ctx context.Context, id string) (workflow.WorkflowRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok || r.TenantID != tid {
		return workflow.WorkflowRun{}, workflow.ErrRunNotFound
	}
	return cloneRun(r), nil
}

func (s *Store) UpdateRun(ctx context.Context, in workflow.WorkflowRun) (workflow.WorkflowRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return workflow.WorkflowRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[in.ID]; !ok || r.TenantID != tid {
		return workflow.WorkflowRun{}, workflow.ErrRunNotFound
	}
	in.TenantID = tid
	s.runs[in.ID] = cloneRun(in)
	return cloneRun(in), nil
}

// ListActiveRuns 全表 running/paused（Sweep 启动恢复专用，平台级调用带各租户 ctx 之外的聚合视图）。
func (s *Store) ListActiveRuns(ctx context.Context) ([]workflow.WorkflowRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]workflow.WorkflowRun, 0)
	for _, r := range s.runs {
		if r.Status == workflow.StatusRunning || r.Status == workflow.StatusPaused {
			out = append(out, cloneRun(r))
		}
	}
	return out, nil
}

func (s *Store) ListRuns(ctx context.Context, workflowID string) ([]workflow.WorkflowRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]workflow.WorkflowRun, 0)
	for _, r := range s.runs {
		if r.TenantID == tid && (workflowID == "" || r.WorkflowID == workflowID) {
			out = append(out, cloneRun(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
