// Package change 变更管理领域：单个变更（Change）与集成批次（IntegrationBatch）。
// 变更生命周期：open -> integrated -> tested -> released（或 reverted/abandoned）。
package change

import (
	"fmt"
	"regexp"
	"strings"
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

// Change 单个变更（工作分支粒度）。json 标签 camelCase（与 workload/pipeline 一致）。
type Change struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenantId,omitempty"`
	AppID         string    `json:"appId"`
	RepoID        string    `json:"repoId"`
	Title         string    `json:"title"`
	Type          string    `json:"type"`
	Branch        string    `json:"branch"`
	BranchCreated bool      `json:"branchCreated"`
	BaseBranch    string    `json:"baseBranch"`
	Status        string    `json:"status"` // open|integrated|tested|released|reverted|abandoned
	BatchID       string    `json:"batchId"`
	ConflictWith  string    `json:"conflictWith"` // integrate 冲突时记前一个变更 ID
	CreatedBy     string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// IntegrationBatch 一次集成批次（同 repo 多变更合并到一个集成分支统一构建发布）。
type IntegrationBatch struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId,omitempty"`
	AppID      string    `json:"appId"`
	RepoID     string    `json:"repoId"`
	Title      string    `json:"title"`
	Branch     string    `json:"branch"`
	Status     string    `json:"status"`
	ChangeIDs  []string  `json:"changeIds"` // 有序
	PipelineID string    `json:"pipelineId"`
	RunID      string    `json:"runId"`
	ReleaseIDs []string  `json:"releaseIds"`
	CreatedBy  string    `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// branchPattern 分支名安全校验（与 devops.CodeRepo.Validate 同款）：
// 仅安全字符（含 -）+ 拒 `-` 前缀（防 git argv flag 注入）。
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// reservedBranches 保留分支：批次集成分支不能撞主干名——Integrate 会「删重建集成分支」，
// branch="main" 会删除仓库 main 分支（paas-bot 是 Gitea admin 无分支保护），必须拒。
var reservedBranches = map[string]bool{"main": true, "master": true}

// validateBranchName 分支名校验：非空 + 安全字符 + 非保留字（深度审计第 1 轮 I-1）。
func validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch 不能为空")
	}
	if !branchPattern.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch 含非法字符（仅字母数字 ._ /）且不能以 - 开头")
	}
	if reservedBranches[strings.ToLower(branch)] {
		return fmt.Errorf("branch 不能使用保留名 %s（主干分支）", branch)
	}
	return nil
}

// Validate 校验变更必填字段：title/type 非空、type ∈ {feat,hotfix}、branch 安全校验。
// BranchCreated=false 时仍要求 branch 非空（引用已有分支）。
func (c Change) Validate() error {
	if c.Title == "" {
		return fmt.Errorf("title 不能为空")
	}
	if c.Type != ChangeFeat && c.Type != ChangeHotfix {
		return fmt.Errorf("type 仅支持 feat|hotfix")
	}
	return validateBranchName(c.Branch)
}

// ValidateBatch 校验批次必填字段：title 非空 + branch 安全校验（拒主干名，防 Integrate 删主干）。
func (b IntegrationBatch) ValidateBatch() error {
	if b.Title == "" {
		return fmt.Errorf("title 不能为空")
	}
	return validateBranchName(b.Branch)
}
