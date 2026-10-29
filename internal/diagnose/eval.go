// 诊断评测集（spec impl-spec S6.1）：≥15 个已知故障，跑 §7 指标
// （Top-1 ≥70% / Top-3 ≥90%、P50<30s / P95<90s、token P50<10k）。
//
// 用例来源全部是本项目真实踩过的故障（CLAUDE.md / 审计记录 / handoff 实地演练），
// 不是虚构场景——评测集的价值在「已知根因可机器判定」，虚构用例无法判定。
// 期望判定：ExpectedCategory 精确匹配 verdict.category；
// ExpectedKeywords 任一命中 Top-1 假设文本（hypothesis + 首条 evidence.detail）。
//
// 真跑依赖真集群 + 真模型（fake client 测不出四坑，同理测不出诊断质量）；
// 本文件锁用例结构与可静态校验的完整性（TestEvalCasesSanity）。
package diagnose

import "fmt"

// EvalCase 单个评测用例。
type EvalCase struct {
	ID               string     // 用例 ID（如 EV-01）
	Title            string     // 故障标题
	Target           DiagTarget // 诊断目标（ns/workload/symptom）
	ExpectedCategory string     // 期望 verdict.category
	ExpectedKeywords []string   // Top-1 假设应命中的关键词（任一）
	Source           string     // 故障来源（真实记录出处）
}

// EvalCases 返回全部评测用例（≥15，spec §7 门槛）。
//
// 分布：code_bug 3 / config_error 5 / resource_exhaustion 2 / dependency_failure 3 /
// infra 2 —— 覆盖 §3 全部 category + §8 四坑各自的暴露面。
func EvalCases() []EvalCase {
	return []EvalCase{
		// —— code_bug ——
		{"EV-01", "paas-core 乐观锁冲突（reconcile 持续失败但 Pod 全绿）",
			DiagTarget{Namespace: "paas", Workload: "paas-core", Symptom: "未知"},
			"code_bug", []string{"乐观锁", "conflict", "resourceVersion", "has been modified"},
			"handoff §10 实地演练（近 1h 报错 22 次，纯 kubectl get pod 发现不了）"},
		{"EV-02", "MCP 工具调用失败未标 isError，工作流吞错伪成功",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-chatbot", Symptom: "错误率"},
			"code_bug", []string{"isError", "MCP", "工具调用"},
			"shop 退款伪成功修复（memory 2026-09-06）"},
		{"EV-03", "usePolling active 条件拦首拉，列表永远 loading",
			DiagTarget{Namespace: "paas", Workload: "paas-core", Symptom: "未知"},
			"code_bug", []string{"轮询", "polling", "loading"},
			"Agent 运行 tab「慢」真因（memory usePolling-active-deadlock）"},

		// —— config_error ——
		{"EV-04", "test workload 缺 DATABASE_URL 起动即退（绑定注入落错 env 桶）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-frontend", Symptom: "CrashLoop"},
			"config_error", []string{"DATABASE_URL", "环境变量", "env", "绑定"},
			"paas-shop setup.sh 四缺口之二（2026-10-21）"},
		{"EV-05", "MCP serverURL 端口错配（:8080 指向无监听端口 Service）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-bff", Symptom: "错误率"},
			"config_error", []string{"8080", "端口", "MCP", "连接拒绝"},
			"paas-shop 重拉起：8 工具 GET+PUT 修复（2026-10-21）"},
		{"EV-06", "NetPol 白名单误伤沙箱出网（agent 拉代码失败）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-agent", Symptom: "未知"},
			"config_error", []string{"NetworkPolicy", "NetPol", "出网", "网络策略"},
			"智能研发泳道示例三坑之一（2026-10-22）"},
		{"EV-07", "metricsPath 未声明，应用指标永久 down target",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-frontend", Symptom: "未知"},
			"config_error", []string{"metricsPath", "scrape", "注解", "采集"},
			"指标采集 opt-in 语义反转（2026-09-24，nginx unsupported Content-Type）"},
		{"EV-08", "appconfig 变更不触发已建 CR 的 env 重渲染（restart 被 superseded）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-statsworker", Symptom: "未知"},
			"config_error", []string{"appconfig", "环境变量", "重渲染", "配置"},
			"paas-shop 运维侧修复两例之一（2026-10-21）"},

		// —— resource_exhaustion ——
		{"EV-09", "Pod OOMKilled（BestEffort 无 memory limit）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-sweeper", Symptom: "CrashLoop"},
			"resource_exhaustion", []string{"OOM", "memory", "limit", "137", "内存"},
			"生产禁 BestEffort 双入口的立项动机（标准基线章）"},
		{"EV-10", "容器内存逼近 limit（512Mi 用 41%→爬升）",
			DiagTarget{Namespace: "paas", Workload: "paas-core", Symptom: "未知"},
			"resource_exhaustion", []string{"memory", "内存", "limit", "512"},
			"handoff §10 附带证据（41/512Mi medium 权重）"},

		// —— dependency_failure ——
		{"EV-11", "应用依赖的 PostgreSQL 未就绪（连接拒绝）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-bff", Symptom: "CrashLoop"},
			"dependency_failure", []string{"postgres", "5432", "connection refused", "数据库"},
			"数据服务三模式常见形态（dataservice 引擎目录设计）"},
		{"EV-12", "上游 LLM 网关 503 降级（凭证未配）",
			DiagTarget{Namespace: "paas", Workload: "paas-core", Symptom: "错误率"},
			"dependency_failure", []string{"503", "airouter", "凭证", "上游"},
			"MaaS 未配凭证 503 降级不 panic 条目"},
		{"EV-13", "registry 不可达致 ImagePullBackOff（集群内 registry NodePort）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-frontend", Symptom: "CrashLoop"},
			"dependency_failure", []string{"ImagePull", "registry", "拉取", "30050"},
			"集群内自建 registry 踩坑史（arm64/amd64 + insecure 配置）"},

		// —— infra ——
		{"EV-14", "僵尸 Service 抢名致流量打到旧版本（chat 503）",
			DiagTarget{Namespace: "paas-t-acme", Workload: "wl-shop-chatbot", Symptom: "错误率"},
			"infra", []string{"Service", "抢名", "僵尸", "selector"},
			"paas-shop chat 503 根因（memory 2026-09-02）+ label 语义漂移"},
		{"EV-15", "node-exporter 9100 被物理网阻断（39 target down）",
			DiagTarget{Namespace: "observability", Workload: "node-exporter", Symptom: "未知"},
			"infra", []string{"9100", "node", "阻断", "网络"},
			"Prometheus down target 三类根因之一（2026-09-24 换 9200 解决）"},
	}
}

// EvalVerdict 评测判定：单用例的期望 vs 实际报告。
type EvalVerdict struct {
	CaseID      string
	HitCategory bool // ExpectedCategory == Report.Verdict.Category
	HitTop1     bool // ExpectedKeywords 任一命中 Top-1 假设文本
	HitTop3     bool // 任一命中前三假设文本（§7 Top-3 口径）
}

// Grade 判定单个报告对单条用例的命中（Top-1/Top-3）。
func Grade(c EvalCase, rep Report) EvalVerdict {
	v := EvalVerdict{CaseID: c.ID, HitCategory: rep.Verdict.Category == c.ExpectedCategory}
	texts := hypothesisTexts(rep, 3)
	if len(texts) > 0 && matchAny(texts[0], c.ExpectedKeywords) {
		v.HitTop1 = true
	}
	for _, txt := range texts {
		if matchAny(txt, c.ExpectedKeywords) {
			v.HitTop3 = true
		}
	}
	return v
}

func hypothesisTexts(rep Report, topN int) []string {
	var out []string
	for i, h := range rep.RootCauseHypotheses {
		if i >= topN {
			break
		}
		txt := h.Hypothesis
		if len(h.Evidence) > 0 {
			txt += " " + h.Evidence[0].Detail
		}
		out = append(out, txt)
	}
	return out
}

func matchAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if kw != "" && containsFold(text, kw) {
			return true
		}
	}
	return false
}

func containsFold(s, substr string) bool {
	if len(substr) == 0 {
		return false
	}
	ls, lsub := lower(s), lower(substr)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// GradeSummary 评测汇总（§7 指标口径）。
type GradeSummary struct {
	Total    int
	Top1Hits int
	Top3Hits int
	CatHits  int
}

func (g GradeSummary) String() string {
	return fmt.Sprintf("total=%d top1=%d(%.0f%%) top3=%d(%.0f%%) category=%d(%.0f%%)",
		g.Total, g.Top1Hits, pct(g.Top1Hits, g.Total), g.Top3Hits, pct(g.Top3Hits, g.Total), g.CatHits, pct(g.CatHits, g.Total))
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
