// Package pg 提供 PostgreSQL 持久化基建：连接池（pgx）与迁移（golang-migrate）。
// 业务模块的 PG 实现按需 import 本包获取 *DB；migration SQL 放 migrations/，启动时自动 up。
// 默认 PAAS_DB_URL 为空时 core 走内存后端，本包不被引用（dev/echo 零依赖路径）。
package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB 封装 pgxpool 连接池。业务仓储通过 Pool() 取 *pgxpool.Pool 执行查询。
type DB struct {
	pool *pgxpool.Pool
	dsn  string // 供迁移驱动复用（stdlib.OpenDBFromPool 需要 pool，此处保留 dsn 便于调试）
}

// Open 创建连接池并 ping 校验连通性。
// dsn 形如 postgres://paas:pwd@host:5432/db?sslmode=disable。
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库连通性校验失败: %w", err)
	}
	return &DB{pool: pool, dsn: dsn}, nil
}

// Pool 返回底层连接池（业务仓储用）。
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close 释放连接池。进程退出路径调用。
func (d *DB) Close() {
	if d != nil && d.pool != nil {
		d.pool.Close()
	}
}
