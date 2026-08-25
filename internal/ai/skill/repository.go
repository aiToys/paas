package skill

import "context"

// Repository skill 持久化接口（全方法 ctx + tenant 过滤；ListAll 供 admin 跨租户总览）。
type Repository interface {
	List(ctx context.Context) ([]Skill, error)
	ListAll(ctx context.Context) ([]Skill, error) // admin 跨租户（带 TenantID）
	Get(ctx context.Context, id string) (Skill, error)
	Create(ctx context.Context, s Skill) (Skill, error)
	Update(ctx context.Context, s Skill) (Skill, error)
	Delete(ctx context.Context, id string) error
	SkillsCount(ctx context.Context) (int, error) // 全表，seed 判空
}
