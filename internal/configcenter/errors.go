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
)
