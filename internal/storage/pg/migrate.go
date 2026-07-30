package pg

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres" // postgres 驱动（WithInstance）
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/stdlib"
)

// migrationsFS 嵌入 migrations/ 目录的 SQL 文件，编译进二进制，部署无需额外文件。
//
//go:embed all:migrations
var migrationsFS embed.FS

// ErrNoMigrations 表示当前 schema 已是最新（无待执行迁移），非错误。
var ErrNoMigrations = migrate.ErrNoChange

// RunMigrations 把 pgxpool 桥接为 database/sql，用 golang-migrate 跑所有 up 迁移。
// 幂等：已是最新时返回 nil（ErrNoChange 归一化为 nil）。
func RunMigrations(ctx context.Context, db *DB) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("加载迁移源失败: %w", err)
	}
	// pgxpool → *sql.DB（migrate 的 postgres 驱动基于 database/sql）。
	sqlDB := stdlib.OpenDBFromPool(db.pool)
	defer func() { _ = sqlDB.Close() }()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("迁移前连通性校验失败: %w", err)
	}

	// *sql.DB → migrate database.Driver（postgres 方言）。
	var drv database.Driver
	if drv, err = postgres.WithInstance(sqlDB, &postgres.Config{}); err != nil {
		return fmt.Errorf("初始化迁移数据库驱动失败: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		return fmt.Errorf("初始化迁移失败: %w", err)
	}
	defer func() { _, _ = m.Close() }() // migrate.Close 返回 (sourceErr, dbErr)，迁移已校验，忽略

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行迁移失败: %w", err)
	}
	return nil
}
