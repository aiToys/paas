package configcenter

import "errors"

// 领域 sentinel 错误（handler/存储层统一 errors.Is 判定，替代脆弱的中文文本匹配）。
var (
	// ErrNamespaceNotFound 命名空间不存在（跨租户/已删统一归此，不泄漏存在性）。
	ErrNamespaceNotFound = errors.New("命名空间不存在")
	// ErrNamespaceNameTaken 命名空间名已被占用（租户内唯一约束冲突，handler 映射 409）。
	ErrNamespaceNameTaken = errors.New("命名空间名被占用")
	// ErrItemNotFound 配置项不存在。
	ErrItemNotFound = errors.New("配置项不存在")
	// ErrPublishNotFound 发布不存在（跨租户/已删统一归此）。
	ErrPublishNotFound = errors.New("发布不存在")
	// ErrPublishAlreadyActive 发布已是当前生效版本（回滚拒绝，handler 映射 409）。
	ErrPublishAlreadyActive = errors.New("发布已是当前生效版本")
	// ErrNoChanges 新快照与当前 active 完全一致（空发布拒绝，handler 映射 409）——
	// 防无变更重复发布虚涨版本号、污染发布历史与回滚目标。
	ErrNoChanges = errors.New("配置无变更，无需发布")
)

// ErrLaneOverrideNotFound 泳道配置覆盖不存在（删除不存在的 key 时返回）。
var ErrLaneOverrideNotFound = errors.New("泳道配置覆盖不存在")

// 共享配置引用（shared ns → 应用派生 ns）。
var (
	// ErrRefNotFound 引用不存在（解除不存在的引用时返回）。
	ErrRefNotFound = errors.New("共享配置引用不存在")
	// ErrRefExists 引用已存在（同 app ns 对同一 shared ns 重复引用，handler 映射 409）。
	ErrRefExists = errors.New("已引用该共享配置")
	// ErrRefNotShared 被引用 ns 非 shared scope（app 派生 ns 不可被引用，拒 400）。
	ErrRefNotShared = errors.New("目标命名空间不是共享配置")
)
