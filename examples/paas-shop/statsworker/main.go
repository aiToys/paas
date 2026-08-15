// paas-shop 统计 worker：演示「CronJob 定时任务 + appconfig 双向」。
//
// 平台创建 type=cronjob workload（schedule */10 * * * *）-> schedule 到点拉起 Pod -> 跑本程序 -> 退出。
// 职责：连 product DB 聚合统计（分类商品数 + 总数）-> 回写 appconfig STATS_* -> 退出。
// appconfig 通常只读注入 workload，本 worker 演示「业务回写 appconfig」反向能力。
//
// 环境变量（平台绑定/注入）：
//
//	DATABASE_URL       - shop-db 绑定注入
//	PAAS_APPCONFIG_URL - 平台 core base（http://paas-core.paas.svc.cluster.local）
//	PAAS_API_KEY       - 写 appconfig 权限的 Key（appconfig secret 注入）
//	PAAS_APP_ID        - 应用 ID（默认 paas-shop）
//	PAAS_ENV_ID        - 环境 ID
//	PAAS_STATS_INTERVAL - 缺省跑一次退出（CronJob 单次语义）；非空（如 10m）则循环 sleep，供 service 模式可选
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

// httpClient 回写 appconfig 用（带超时，防 core 不可达时 CronJob 挂死）。
var httpClient = &http.Client{Timeout: 10 * time.Second}

func main() {
	shutdown := observ.Init("paas-shop-statsworker")
	defer shutdown()

	interval := os.Getenv("PAAS_STATS_INTERVAL")
	if interval == "" {
		// CronJob 单次语义：跑一次退出
		if err := runOnce(); err != nil {
			slog.Error("statsworker 失败", "err", err)
			os.Exit(1)
		}
		return
	}
	// service 模式（可选）：循环跑
	d, err := time.ParseDuration(interval)
	if err != nil {
		slog.Error("PAAS_STATS_INTERVAL 解析失败（如 10m）", "value", interval, "err", err)
		os.Exit(1)
	}
	for {
		if err := runOnce(); err != nil {
			slog.Warn("本轮统计失败（下轮重试）", "err", err)
		}
		time.Sleep(d)
	}
}

// runOnce 一轮完整流程：连 DB -> 聚合 -> 回写 appconfig。
func runOnce() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL 未设置")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("pgx open: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, total, err := aggregateStats(ctx, db)
	if err != nil {
		return fmt.Errorf("聚合统计失败: %w", err)
	}
	slog.Info("统计完成", "total", total, "categories", summary)

	// 回写 appconfig（STATS_CATEGORY_SUMMARY + STATS_TOTAL_PRODUCTS）
	base := os.Getenv("PAAS_APPCONFIG_URL")
	apiKey := os.Getenv("PAAS_API_KEY")
	appID := os.Getenv("PAAS_APP_ID")
	if appID == "" {
		appID = "paas-shop"
	}
	envID := os.Getenv("PAAS_ENV_ID")
	if base == "" || apiKey == "" || envID == "" {
		slog.Warn("appconfig 回写跳过（PAAS_APPCONFIG_URL/PAAS_API_KEY/PAAS_ENV_ID 未配）")
		return nil
	}
	summaryJSON, _ := json.Marshal(buildSummaryJSON(summary, total))
	if err := postAppConfig(base, apiKey, appID, envID, "STATS_CATEGORY_SUMMARY", string(summaryJSON)); err != nil {
		slog.Warn("回写 STATS_CATEGORY_SUMMARY 失败", "err", err)
	}
	if err := postAppConfig(base, apiKey, appID, envID, "STATS_TOTAL_PRODUCTS", fmt.Sprintf("%d", total)); err != nil {
		slog.Warn("回写 STATS_TOTAL_PRODUCTS 失败", "err", err)
	}
	slog.Info("appconfig 回写完成")
	return nil
}

// CategoryStat 分类统计：商品数 + 库存合计。
type CategoryStat struct {
	Count int `json:"count"`
	Stock int `json:"stock"`
}

// aggregateStats 聚合：分类商品数 + 库存 + 总数。
func aggregateStats(ctx context.Context, db *sql.DB) (map[string]CategoryStat, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT category, count(*), sum(stock) FROM products GROUP BY category`)
	if err != nil {
		return nil, 0, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	summary := map[string]CategoryStat{}
	total := 0
	for rows.Next() {
		var cat string
		var n, stock int
		if err := rows.Scan(&cat, &n, &stock); err != nil {
			return nil, 0, err
		}
		summary[cat] = CategoryStat{Count: n, Stock: stock}
		total += n
	}
	return summary, total, rows.Err()
}

// buildSummaryJSON 构造回写的 JSON 结构（可测纯函数）。
func buildSummaryJSON(summary map[string]CategoryStat, total int) map[string]any {
	return map[string]any{
		"categories": summary,
		"total":      total,
		"at":         time.Now().UTC().Format(time.RFC3339),
	}
}

// postAppConfig 回写一条 appconfig（POST /api/applications/{app}/configs）。
func postAppConfig(baseURL, apiKey, appID, envID, key, value string) error {
	body, _ := json.Marshal(map[string]any{
		"key":   key,
		"value": value,
		"type":  "env",
		"envId": envID,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/api/applications/"+appID+"/configs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("appconfig 回写 %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}
