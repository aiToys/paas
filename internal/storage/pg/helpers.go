// Package pg 共享辅助：错误映射、租户解析、行扫描抽象。
// 各业务模块的 pg 子包引用，避免 11 处重复定义（DRY）。
package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitoys/paas/pkg/tenant"
)

// ErrAlreadyExists 表示主键/唯一键冲突，映射为与内存实现一致的「已存在」错误。
var ErrAlreadyExists = errors.New("已存在")

// IsUniqueViolation 判断 PG 唯一约束冲突（主键或 UNIQUE，SQLSTATE 23505）。
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// FormatExists 把实体名拼进「已存在」错误，如 FormatExists("应用") → "应用已存在"。
func FormatExists(what string) error { return fmt.Errorf("%s%w", what, ErrAlreadyExists) }

// TenantOrErr 从 ctx 取租户 ID；缺失返错误（fail-closed）。
// 委托 pkg/tenant.IDOrErr（单一真源），保留本包签名供各 pg 子包复用（已广泛引用）。
func TenantOrErr(ctx context.Context) (string, error) {
	return tenant.IDOrErr(ctx)
}

// RowScanner 抽象 pgx QueryRow 与 Row 的 Scan 来源，供 scan 辅助函数复用。
type RowScanner interface {
	Scan(dest ...any) error
}
