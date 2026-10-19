package prompt

import "context"

// Repository prompt 持久化接口（全方法 ctx + tenant 过滤）。
// Create 同 name 自动 version+1 + 激活（旧版 deactive）。GetActive 取当前激活版本。
// CreateDraft 只递增版本不激活（现有 active 不动）——显式激活语义（AI 资产专项 C3），
// 激活走 SetActive（前端激活确认时展示引用影响）。
type Repository interface {
	List(ctx context.Context) ([]Prompt, error)         // 列全部版本（按 name, version desc）
	ListAll(ctx context.Context) ([]Prompt, error)      // admin 跨租户（带 TenantID）
	Get(ctx context.Context, id string) (Prompt, error) // 取单版本
	Create(ctx context.Context, p Prompt) (Prompt, error)
	CreateDraft(ctx context.Context, p Prompt) (Prompt, error) // 同 Create 但不激活、不动现有 active
	SetActive(ctx context.Context, id string) (Prompt, error)  // 激活某版本（同 name 其他 deactive）
	Delete(ctx context.Context, id string) error               // 删单版本
	GetActive(ctx context.Context, name string) (Prompt, error)
	PromptsCount(ctx context.Context) (int, error) // 全表，seed 判空
}
