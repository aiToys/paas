// Package change 变更管理领域：单个变更（Change）与集成批次（IntegrationBatch）。
// 变更生命周期：open -> integrated -> tested -> released（或 reverted/abandoned）。
package change

import (
	"fmt"
	"time"
)

// 变更类型（仅 feat|hotfix，与 trunk-based 短分支模型对齐）。
const (
	ChangeFeat   = "feat"
	ChangeHotfix = "hotfix"
)

// 变更状态。
const (
	ChangeOpen       = "open"       // 已创建（分支已建或引用已有分支）
	ChangeIntegrated = "integrated" // 已合入集成分支
	ChangeTested     = "tested"     // 批次测试通过
	ChangeReleased   = "released"   // 随批次发布
	ChangeReverted   = "reverted"   // 被回退
	ChangeAbandoned  = "abandoned"  // 被放弃
)

// 集成批次状态。
const (
	BatchCollecting = "collecting" // 收集变更中
	BatchBuilding   = "building"   // 构建中
	BatchConflict   = "conflict"   // 集成冲突
	BatchTesting    = "testing"    // 测试中
	BatchTested     = "tested"     // 测试通过
	BatchReleasing  = "releasing"  // 发布中
	BatchReleased   = "released"   // 已发布
	BatchFailed     = "failed"     // 失败（终态）
	BatchAbandoned  = "abandoned"  // 已放弃（终态）
)

// Change 单个变更（工作分支粒度）。
type Change struct {
	ID, TenantID, AppID, RepoID, Title, Type, Branch string
	BranchCreated bool
	BaseBranch    string
	Status        string // open|integrated|tested|released|reverted|abandoned
	BatchID       string
	ConflictWith  string // integrate 冲突时记前一个变更 ID
	CreatedBy     string
	CreatedAt, UpdatedAt time.Time
}

// IntegrationBatch 一次集成批次（同 repo 多变更合并到一个集成分支统一构建发布）。
type IntegrationBatch struct {
	ID, TenantID, AppID, RepoID, Title, Branch, Status string
	ChangeIDs  []string // 有序
	PipelineID, RunID string
	ReleaseIDs []string
	CreatedBy  string
	CreatedAt, FinishedAt time.Time
}

// Validate 校验变更必填字段：title/type/branch 非空、type ∈ {feat,hotfix}。
// BranchCreated=false 时仍要求 branch 非空（引用已有分支）。
func (c Change) Validate() error {
	if c.Title == "" {
		return fmt.Errorf("title 不能为空")
	}
	if c.Type != ChangeFeat && c.Type != ChangeHotfix {
		return fmt.Errorf("type 仅支持 feat|hotfix")
	}
	if c.Branch == "" {
		return fmt.Errorf("branch 不能为空")
	}
	return nil
}

// ValidateBatch 校验批次必填字段：title/branch 非空。
func (b IntegrationBatch) ValidateBatch() error {
	if b.Title == "" {
		return fmt.Errorf("title 不能为空")
	}
	if b.Branch == "" {
		return fmt.Errorf("branch 不能为空")
	}
	return nil
}
