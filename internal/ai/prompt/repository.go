package prompt

import "context"

// Repository prompt 持久化接口（全方法 ctx + tenant 过滤）。
// Create 同 name 自动 version+1 + 激活（旧版 deactive）。GetActive 取当前激活版本。
type Repository interface {
	List(ctx context.Context) ([]Prompt, error)        // 列全部版本（按 name, version desc）
	Get(ctx context.Context, id string) (Prompt, error) // 取单版本
	Create(ctx context.Context, p Prompt) (Prompt, error)
	SetActive(ctx context.Context, id string) (Prompt, error) // 激活某版本（同 name 其他 deactive）
	Delete(ctx context.Context, id string) error              // 删单版本
	GetActive(ctx context.Context, name string) (Prompt, error)
	PromptsCount(ctx context.Context) (int, error) // 全表，seed 判空
}
