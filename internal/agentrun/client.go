// Package agentrun 是 agentrun 执行域的 paas 侧接入（编排通道 client + 观测代理）。
//
// 集成契约：agent-bianpai/docs/paas-integration-contract.md（v1，§5 已按
// docs/superpowers/specs/2026-10-11-agentrun-pipeline-integration-design.md 修订）。
// 依赖方向：paas → agentrun 单向（编排=静态 token server-to-server；观测=用户 JWT）；
// ns 一律服务端强制 agent（ns=租户边界，剥离客户端传入）。
package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/httputil"
)

// 编排通道权限无关（server-to-server）；观测通道权限点。
const (
	PermRead  = "agentrun:read"
	PermWrite = "agentrun:write"
)

// Namespace 契约 v1 公共命名空间（服务端强制，不接受客户端传入；租户专属留 Phase 后续）。
const Namespace = "agent"

// RepoResolver app→git 仓库 URL（cmd/core 桥接 giteaBridge：owner/repo + Gitea base URL 组装 .git 地址）。
type RepoResolver interface {
	GitURL(ctx context.Context, appID string) (string, error)
}

// Client 是 agentrun 编排通道 HTTP adapter（实现 pipeline.AgentRunner）。
// 持有静态 token（契约 §1.3 CI/管理员通道）：后台 stage 执行期间用户 JWT（15min）会过期，不能透传。
type Client struct {
	baseURL string
	token   string
	repos   RepoResolver
	http    *http.Client
}

// NewClient 创建编排通道 client；baseURL/token 来自 env PAAS_AGENTRUN_URL/PAAS_AGENTRUN_TOKEN。
func NewClient(baseURL, token string, repos RepoResolver) *Client {
	return &Client{baseURL: baseURL, token: token, repos: repos, http: httputil.NewClient(30 * time.Second)}
}

// CreateRun 提交单次执行（契约 §4.1 白名单字段；白名单外键不发送——注入防护）。
// project.git 经 RepoResolver 解析（engine 只传领域语义，repo 解析归本 adapter）。
func (c *Client) CreateRun(ctx context.Context, spec pipeline.AgentTaskSpec) (pipeline.AgentTaskRef, error) {
	return c.createRun(ctx, c.token, spec)
}

// CreateRunAsUser 委派按钮通道（契约 v2 §3.3/§5.1）：透传用户 access JWT（agentrun 本地验签，
// 归因 claims.sub）——区别于编排通道的静态 token（归因 agent:<run名>）。
func (c *Client) CreateRunAsUser(ctx context.Context, userJWT string, spec pipeline.AgentTaskSpec) (pipeline.AgentTaskRef, error) {
	return c.createRun(ctx, userJWT, spec)
}

// AgentPipelineSpec 委派两阶段 pipeline 提交语义（implement 硬依赖前序 analyze 产物——
// 单 run implement 会失败，委派必须走 pipeline；console 0.14.1 起支持 workBranch/project）。
type AgentPipelineSpec struct {
	AppID      string
	Request    string
	Runtime    string   // 缺省 codex
	WorkBranch string   // 委派分支（变更分支）
	BaseBranch string   // 基线
	Stages     []string // 委派固定 ["analyze","implement"]
}

// CreatePipelineAsUser 委派按钮 v2：POST /api/pipelines（stages+workBranch+project，用户 JWT）。
// 返回 pipeline 名（Change.AgentRunName 存 pipeline 名，观测详情用 /pipelines/{name} 查）。
func (c *Client) CreatePipelineAsUser(ctx context.Context, userJWT string, spec AgentPipelineSpec) (string, error) {
	gitURL, err := c.repos.GitURL(ctx, spec.AppID)
	if err != nil {
		return "", fmt.Errorf("解析应用仓库失败: %w", err)
	}
	runtime := spec.Runtime
	if runtime == "" {
		runtime = "codex"
	}
	body := map[string]any{
		"runtime":    runtime,
		"mode":       "real",
		"request":    spec.Request,
		"stages":     spec.Stages,
		"workBranch": spec.WorkBranch,
		"baseBranch": spec.BaseBranch,
		"project":    map[string]any{"git": gitURL, "ref": spec.WorkBranch},
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := c.doWithToken(ctx, http.MethodPost, "/api/pipelines", userJWT, body, &out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// outputContract 执行域结果约定（2026-10-14 联调定案）：runner 对 Codex 最终回复按
// {"summary","verdict"} 约定解析（平台校验这两个字段），Codex 输出自然语言即判负——
// 曾因 paas 自行约定 {"branch":...} 与平台 schema 不匹配而连续失败；普通措辞下 Codex
// 遵守率概率性（一次通过一次失败），故用强约束+后果说明措辞。分支推送由平台侧产出
// 回写负责（agent 无 git），不在此要求。
const outputContract = "\n\n【最高优先级 · 机器解析契约】全部工作完成后，你的最后一条消息必须精确地是一个 JSON 对象：" +
	"{\"summary\": \"<一句话改动说明>\", \"verdict\": \"pass\"}（未完成或验证不通过则 \"fail\"）。" +
	"系统对最后一条消息做机器解析：若其中包含任何 JSON 之外的文字、markdown 代码块或解释，" +
	"本次运行将被判定为失败，你完成的全部改动将被丢弃且不会推送。除该 JSON 外不要输出任何字符。"

func (c *Client) createRun(ctx context.Context, token string, spec pipeline.AgentTaskSpec) (pipeline.AgentTaskRef, error) {
	gitURL, err := c.repos.GitURL(ctx, spec.AppID)
	if err != nil {
		return pipeline.AgentTaskRef{}, fmt.Errorf("解析应用仓库失败: %w", err)
	}
	runtime := spec.Runtime
	if runtime == "" {
		runtime = "codex"
	}
	stage := spec.Stage
	if stage == "" {
		stage = "implement"
	}
	// 委派模式（v2 §3.3）：project.ref=workBranch（agent 在变更分支上工作）；独立模式 ref=BaseBranch。
	ref := spec.BaseBranch
	body := map[string]any{
		"runtime":  runtime,
		"mode":     "real",
		"stage":    stage,
		"request":  spec.Request + outputContract,
		"priority": 50,
		"retries":  0,
	}
	if spec.Name != "" { // 空=让 agentrun 自动生成（委派场景，run 名以执行域返回为准）
		body["name"] = spec.Name
	}
	if spec.WorkBranch != "" {
		ref = spec.WorkBranch
		body["workBranch"] = spec.WorkBranch
	}
	body["project"] = map[string]any{"git": gitURL, "ref": ref}
	if spec.DeadlineSeconds > 0 {
		body["deadlineSeconds"] = spec.DeadlineSeconds
	}
	if spec.WorkspaceFrom != "" {
		body["workspaceFrom"] = spec.WorkspaceFrom
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := c.doWithToken(ctx, http.MethodPost, "/api/runs", token, body, &out); err != nil {
		return pipeline.AgentTaskRef{}, err
	}
	return pipeline.AgentTaskRef{Name: out.Name}, nil
}

// runResp agentrun GET /api/runs/{name} 响应。
// 2026-10-11 对方会话确认字段已落地：workBranch（委派入参回显）+ producedBranch（独立模式产出分支）。
type runResp struct {
	Name           string `json:"name"`
	Phase          string `json:"phase"`   // Pending|Running|Succeeded|Failed
	Verdict        string `json:"verdict"` // pass|test_failure|...
	Branch         string `json:"branch"`
	WorkBranch     string `json:"workBranch"`     // 委派模式入参回显
	ProducedBranch string `json:"producedBranch"` // 独立模式产出分支（agentrun/<run名>，merge 后有值）
	Tokens         int64  `json:"tokens"`
	Message        string `json:"message,omitempty"`
	Error          string `json:"error,omitempty"` // runner 判负原因（如「Codex 未返回结构化结果」）；成功路径为空
}

// PollRun 阻塞轮询到终态（succeeded/failed 返回状态非 error；上游/ctx 错误返回 error）。
// PollRun 单次查询当前状态（running 则原样返回非终态 phase）。轮询节奏与超时兜底归
// engine.pollAgent——此前本方法内部自带无限轮询循环，对方平台超时不守约（deadline 到期
// 仍 running，2026-10-13 实测 22min）时 engine 的 deadline 分支永远轮不到，goroutine 挂死
// 占死单实例串行槽位。
func (c *Client) PollRun(ctx context.Context, ref pipeline.AgentTaskRef) (pipeline.AgentTaskStatus, error) {
	var out runResp
	if err := c.do(ctx, http.MethodGet, "/api/runs/"+ref.Name, nil, &out); err != nil {
		return pipeline.AgentTaskStatus{}, err
	}
	switch out.Phase {
	case "succeeded", "Succeeded", "failed", "Failed":
		// Failed 时对方的失败原因在 error 字段（message 为空）——并入 Message 让
		// StageRun.Error 有内容（曾显示「agent run 未通过: 」空文案，排障断链 2026-10-14）。
		msg := out.Message
		if out.Error != "" {
			if msg != "" {
				msg += ": "
			}
			msg += out.Error
		}
		return pipeline.AgentTaskStatus{
			Phase: lower(out.Phase), Verdict: out.Verdict,
			Branch:  firstNonEmpty(out.ProducedBranch, out.Branch), // 产出分支：producedBranch（定稿）优先，branch 兼容
			Message: msg, Tokens: out.Tokens,
		}, nil
	}
	return pipeline.AgentTaskStatus{Phase: lower(out.Phase)}, nil // running 等非终态原样透传
}

// CancelRun Abort 级联取消（engine 的 pollAgent 用脱离取消的 ctx 调用）。
func (c *Client) CancelRun(ctx context.Context, ref pipeline.AgentTaskRef) error {
	return c.do(ctx, http.MethodDelete, "/api/runs/"+ref.Name, nil, nil)
}

// RunExists 校验执行域 run 是否存在（Retry 续接前校验 workspaceFrom 引用）：
// paas 超时取消（DELETE）后对方 CR 即删，Retry 的 prevRunName 可能指向已删 run——
// 对方 operator 对失效 workspaceFrom 无限重试不判负（2026-10-14 实测 -r2 卡死）。
// GET 404=false；其他错误上抛由调用方保守保留引用。
func (c *Client) RunExists(ctx context.Context, name string) (bool, error) {
	var out runResp
	if err := c.do(ctx, http.MethodGet, "/api/runs/"+name, nil, &out); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// do 统一请求：ns 服务端强制 + 静态 token + 错误分流（4xx/5xx {"error"} 透传中文；连接错误脱敏）。
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	return c.doWithToken(ctx, method, path, c.token, body, out)
}

// doWithToken token 参数化（静态 token 编排通道 / 用户 JWT 委派按钮通道共用错误分流与 ns 强制）。
func (c *Client) doWithToken(ctx context.Context, method, path, token string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path+"?ns="+Namespace, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent 编排平台不可达: %w", err) // 脱敏：dial tcp 细节不进 StageRun.Error
	}
	defer resp.Body.Close() //nolint:errcheck // 只读流
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error) // agentrun 业务错误（中文可读）透传
		}
		return fmt.Errorf("agentrun %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func lower(s string) string {
	if len(s) > 0 && s[0] >= 'A' && s[0] <= 'Z' {
		return string(s[0]+'a'-'A') + s[1:]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
