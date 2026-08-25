package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/eval"
	"github.com/aitoys/paas/pkg/tenant"
)

type Store struct {
	mu    sync.RWMutex
	cases map[string]eval.EvalCase
	runs  map[string]eval.EvalRun
}

func NewStore() *Store {
	return &Store{cases: make(map[string]eval.EvalCase), runs: make(map[string]eval.EvalRun)}
}

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return "eval-" + hex.EncodeToString(b)
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return "", eval.ErrEvalCaseNotFound
	}
	return tid, nil
}

func clone(c eval.EvalCase) eval.EvalCase { return c }

func (s *Store) List(ctx context.Context, agentID string) ([]eval.EvalCase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]eval.EvalCase, 0)
	for _, c := range s.cases {
		if c.TenantID != tid {
			continue
		}
		if agentID != "" && c.AgentID != agentID {
			continue
		}
		out = append(out, clone(c))
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (eval.EvalCase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return eval.EvalCase{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cases[id]
	if !ok || c.TenantID != tid {
		return eval.EvalCase{}, eval.ErrEvalCaseNotFound
	}
	return clone(c), nil
}

func (s *Store) Create(ctx context.Context, c eval.EvalCase) (eval.EvalCase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return eval.EvalCase{}, err
	}
	if err := c.Validate(); err != nil {
		return eval.EvalCase{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同 Agent 下 Name 唯一（便于识别）。
	if c.Name != "" {
		for _, ex := range s.cases {
			if ex.TenantID == tid && ex.AgentID == c.AgentID && ex.Name == c.Name {
				return eval.EvalCase{}, eval.ErrEvalCaseExists
			}
		}
	}
	if c.ID == "" {
		c.ID = randID()
	}
	c.TenantID = tid
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	s.cases[c.ID] = c
	return clone(c), nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok || c.TenantID != tid {
		return eval.ErrEvalCaseNotFound
	}
	delete(s.cases, id)
	return nil
}

func (s *Store) EvalCasesCount(ctx context.Context) (int, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return len(s.cases), nil // admin 全量统计（无 tenant ctx 时）
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, c := range s.cases {
		if c.TenantID == tid {
			n++
		}
	}
	return n, nil
}

// —— EvalRun 历史（环形截断，每 agent 保留最近 20 次）——
const maxRunsPerAgent = 20

func (s *Store) ListRuns(ctx context.Context, agentID string) ([]eval.EvalRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]eval.EvalRun, 0)
	for _, r := range s.runs {
		if r.TenantID == tid && (agentID == "" || r.AgentID == agentID) {
			out = append(out, r)
		}
	}
	// 最近优先
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetRun(ctx context.Context, id string) (eval.EvalRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return eval.EvalRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok || r.TenantID != tid {
		return eval.EvalRun{}, eval.ErrEvalRunNotFound
	}
	return r, nil
}

func (s *Store) CreateRun(ctx context.Context, in eval.EvalRun) (eval.EvalRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return eval.EvalRun{}, err
	}
	if in.ID == "" {
		in.ID = "evalrun-" + randID()
	}
	in.TenantID = tid
	in.CreatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[in.ID] = in
	// 环形截断：同 agent 仅保留最近 maxRunsPerAgent 次
	var ids []string
	for id, r := range s.runs {
		if r.TenantID == tid && r.AgentID == in.AgentID {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxRunsPerAgent {
		sort.Slice(ids, func(i, j int) bool { return s.runs[ids[i]].CreatedAt.After(s.runs[ids[j]].CreatedAt) })
		for _, id := range ids[maxRunsPerAgent:] {
			delete(s.runs, id)
		}
	}
	return in, nil
}
