package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/devops/pipeline"
)

type fakeRepos struct{ url string }

func (f fakeRepos) GitURL(_ context.Context, _ string) (string, error) { return f.url, nil }

// newFakeAgentrun 起 httptest 假 agentrun：POST /api/runs 创建（断言 body/ns/token）、
// GET /api/runs/{name} 按脚本返回 phase 序列、DELETE 取消。
func newFakeAgentrun(t *testing.T, phases []string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var gotCreateBody map[string]any
	mux := http.NewServeMux()
	pollCount := 0
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ns") != "agent" {
			t.Errorf("POST ns = %s, 期望 agent（服务端强制）", r.URL.Query().Get("ns"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-static" {
			t.Errorf("Authorization = %s, 期望静态 token", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotCreateBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"fake-run-1"}`))
	})
	mux.HandleFunc("GET /api/runs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ns") != "agent" {
			t.Errorf("GET ns = %s, 期望 agent", r.URL.Query().Get("ns"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-static" {
			t.Errorf("GET Authorization = %s", got)
		}
		phase := "succeeded"
		if pollCount < len(phases) {
			phase = phases[pollCount]
		} else if len(phases) > 0 {
			phase = phases[len(phases)-1]
		}
		pollCount++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": r.PathValue("name"), "phase": phase, "verdict": "pass",
			"branch": "auto-xyz", "tokens": 1234,
		})
	})
	mux.HandleFunc("DELETE /api/runs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-static" {
			t.Errorf("DELETE Authorization = %s", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &gotCreateBody
}

func TestClientCreateRunSpec(t *testing.T) {
	srv, bodyPtr := newFakeAgentrun(t, []string{"succeeded"})
	c := NewClient(srv.URL, "tok-static", fakeRepos{url: "http://gitea.paas.svc:3000/bot/shop.git"})

	ref, err := c.CreateRun(context.Background(), pipeline.AgentTaskSpec{
		AppID: "app-1", Name: "r-1-0", Request: "实现取消订单", Runtime: "codex",
		Stage: "implement", BaseBranch: "main", WorkspaceFrom: "r-0-9", DeadlineSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "fake-run-1" {
		t.Errorf("ref.Name = %s, 期望 fake-run-1（以执行域返回为准）", ref.Name)
	}
	b := *bodyPtr
	// request 尾部拼接 outputContract（执行域 {summary,verdict} 解析契约），前缀仍是原始需求
	if req, _ := b["request"].(string); !strings.HasPrefix(req, "实现取消订单") || !strings.Contains(req, `"verdict"`) {
		t.Errorf("request = %q, 期望需求文本+输出格式指令", req)
	}
	if b["runtime"] != "codex" || b["stage"] != "implement" {
		t.Errorf("body 核心 字段 = %v", b)
	}
	if b["name"] != "r-1-0" || b["workspaceFrom"] != "r-0-9" || b["deadlineSeconds"] != float64(300) {
		t.Errorf("body 续接/超时 字段 = %v", b)
	}
	proj, _ := b["project"].(map[string]any)
	if proj == nil || proj["git"] != "http://gitea.paas.svc:3000/bot/shop.git" || proj["ref"] != "main" {
		t.Errorf("project = %v, 期望 GitURL 解析注入", b["project"])
	}
	if b["mode"] != "real" {
		t.Errorf("mode = %v, 期望固定 real", b["mode"])
	}
	// 白名单外键不发送（注入防护对齐契约 §4.1）
	if _, bad := b["authorization"]; bad {
		t.Errorf("白名单外键泄漏: %v", b)
	}
}

func TestClientPollRunToTerminal(t *testing.T) {
	// 单次查询语义：终态直接透传（序列推进归 engine.pollAgent 的 ticker 循环）
	srv, _ := newFakeAgentrun(t, []string{"succeeded"})
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	st, err := c.PollRun(context.Background(), pipeline.AgentTaskRef{Name: "fake-run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "succeeded" || st.Branch != "auto-xyz" || st.Tokens != 1234 {
		t.Errorf("终态 = %+v", st)
	}
}

func TestClientPollRunFailedPhase(t *testing.T) {
	srv, _ := newFakeAgentrun(t, []string{"failed"})
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	st, err := c.PollRun(context.Background(), pipeline.AgentTaskRef{Name: "fake-run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "failed" {
		t.Errorf("phase = %s, 期望 failed（终态归一返回非 error）", st.Phase)
	}
}

func TestClientCancelRun(t *testing.T) {
	srv, _ := newFakeAgentrun(t, nil)
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	if err := c.CancelRun(context.Background(), pipeline.AgentTaskRef{Name: "fake-run-1"}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
}

func TestClientUpstreamErrorSanitized(t *testing.T) {
	// 连接拒绝 → 脱敏中文错误（防内部 dial tcp 文案泄漏进 StageRun.Error）
	c := NewClient("http://127.0.0.1:1", "tok-static", fakeRepos{})
	_, err := c.CreateRun(context.Background(), pipeline.AgentTaskSpec{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "不可达") {
		t.Errorf("连接拒绝应脱敏为「不可达」, got %v", err)
	}
}

func TestClientAPIErrorPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"同名 run 已存在"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	_, err := c.CreateRun(context.Background(), pipeline.AgentTaskSpec{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "同名 run 已存在") {
		t.Errorf("agentrun 业务错误应透传中文文案, got %v", err)
	}
}

func TestClientPollRunningSingleShot(t *testing.T) {
	// PollRun 单次查询语义（2026-10-13）：running 原样返回非终态 phase、不内部循环——
	// 轮询节奏与超时兜底归 engine.pollAgent（此前内部死循环令 engine deadline 永不生效）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "x", "phase": "Running"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	st, err := c.PollRun(context.Background(), pipeline.AgentTaskRef{Name: "x"})
	if err != nil || st.Phase != "running" {
		t.Errorf("running 应原样透传: st=%+v err=%v", st, err)
	}
}

func TestClientPollRespectsContext(t *testing.T) {
	// 已取消的 ctx → 单次 GET 经 do 传播取消，PollRun 立即返回错误（Abort 依赖此语义）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "x", "phase": "running"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-static", fakeRepos{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.PollRun(ctx, pipeline.AgentTaskRef{Name: "x"}); err == nil {
		t.Error("已取消 ctx 应返回错误")
	}
}

// TestClientCreateRunAsUser 委派按钮通道：用户 JWT 归因（区别于编排通道静态 token），
// body 带 workBranch（契约 v2 §3.3 委派模式标志），project.ref=workBranch。
func TestClientCreateRunAsUser(t *testing.T) {
	var gotAuth, gotWork, gotRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotWork, _ = b["workBranch"].(string)
		if proj, ok := b["project"].(map[string]any); ok {
			gotRef, _ = proj["ref"].(string)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"chg-run-1"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-static", fakeRepos{url: "http://g/wing/shop.git"})

	ref, err := c.CreateRunAsUser(context.Background(), "user-jwt-abc", pipeline.AgentTaskSpec{
		AppID: "app-1", Name: "chg-1", Request: "实现取消订单",
		BaseBranch: "main", WorkBranch: "feat/agent-12345",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "chg-run-1" {
		t.Errorf("ref.Name = %s", ref.Name)
	}
	if gotAuth != "Bearer user-jwt-abc" {
		t.Errorf("Authorization = %s, 期望用户 JWT（归因用户 sub）", gotAuth)
	}
	if gotWork != "feat/agent-12345" {
		t.Errorf("workBranch = %q, 期望委派分支透传", gotWork)
	}
	if gotRef != "feat/agent-12345" {
		t.Errorf("project.ref = %q, 期望 workBranch（agent 在变更分支上工作）", gotRef)
	}
}

// TestClientCreatePipelineAsUser 委派按钮 v2（implement 硬依赖前序 analyze 产物，
// 委派必须走两阶段 pipeline——console 0.14.1 已支持 workBranch/project）。
func TestClientCreatePipelineAsUser(t *testing.T) {
	var gotAuth, gotWork string
	var gotStages []string
	var gotGit, gotRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pipelines" {
			t.Errorf("path = %s, 期望 /api/pipelines", r.URL.Path)
		}
		if r.URL.Query().Get("ns") != "agent" {
			t.Errorf("ns = %s", r.URL.Query().Get("ns"))
		}
		gotAuth = r.Header.Get("Authorization")
		var b struct {
			Stages     []string `json:"stages"`
			WorkBranch string   `json:"workBranch"`
			Project    struct {
				Git string `json:"git"`
				Ref string `json:"ref"`
			} `json:"project"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotStages, gotWork, gotGit, gotRef = b.Stages, b.WorkBranch, b.Project.Git, b.Project.Ref
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"pl-chg-1"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-static", fakeRepos{url: "http://g/wing/shop.git"})

	name, err := c.CreatePipelineAsUser(context.Background(), "jwt-u9", AgentPipelineSpec{
		AppID: "app-1", Request: "实现取消订单", WorkBranch: "feat/agent-9", BaseBranch: "main",
		Stages: []string{"analyze", "implement"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "pl-chg-1" {
		t.Errorf("name = %s", name)
	}
	if gotAuth != "Bearer jwt-u9" {
		t.Errorf("Authorization = %s, 期望用户 JWT", gotAuth)
	}
	if len(gotStages) != 2 || gotStages[0] != "analyze" || gotStages[1] != "implement" {
		t.Errorf("stages = %v, 期望 analyze→implement（implement 硬依赖 analyze 产物）", gotStages)
	}
	if gotWork != "feat/agent-9" || gotRef != "feat/agent-9" {
		t.Errorf("workBranch/ref = %s/%s", gotWork, gotRef)
	}
	if gotGit != "http://g/wing/shop.git" {
		t.Errorf("project.git = %s（服务端组装集群内 URL）", gotGit)
	}
}

// TestListRunsOverview admin 总览：裸数组解码 + 来源归类（auto-* 前缀）+ request 截断 + phase/token 汇总。
func TestListRunsOverview(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ns") != "agent" {
			t.Errorf("GET ns = %s, 期望 agent", r.URL.Query().Get("ns"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"auto-review-shop-3","stage":"analyze","phase":"Succeeded","verdict":"pass","tokens":414719,"createdAt":"2026-10-12 15:09:18"},
			{"name":"run-abc-0","stage":"implement","phase":"Succeeded","verdict":"pass","tokens":38638,
			 "request":"` + strings.Repeat("需", 300) + `","summary":"改了 README"},
			{"name":"auto-test-shop-4","stage":"implement","phase":"Failed","tokens":1000,"error":"boom"},
			{"name":"run-xyz-0","stage":"analyze","phase":"Running","tokens":0}
		]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "tok-static", fakeRepos{url: "http://git"})

	ov, err := c.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.Stats.Total != 4 || ov.Stats.Succeeded != 2 || ov.Stats.Failed != 1 || ov.Stats.Running != 1 {
		t.Fatalf("phase 汇总不符: %+v", ov.Stats)
	}
	if ov.Stats.TokensTotal != 454357 || ov.Stats.TokensAuto != 415719 || ov.Stats.TokensPaas != 38638 {
		t.Fatalf("token 汇总不符: %+v", ov.Stats)
	}
	byName := map[string]AdminRun{}
	for _, r := range ov.Runs {
		byName[r.Name] = r
	}
	if byName["auto-review-shop-3"].Source != "auto-review" || byName["auto-test-shop-4"].Source != "auto-test" {
		t.Fatalf("auto-* 归类不符: %+v", byName)
	}
	if byName["run-abc-0"].Source != "paas" {
		t.Fatalf("paas 归类不符: %+v", byName["run-abc-0"])
	}
	// 截断：300 字中文需求 → 200 字 + 省略号（UTF-8 不切半字符）
	if got := byName["run-abc-0"].Request; len([]rune(got)) != 201 || !strings.HasSuffix(got, "…") {
		t.Fatalf("request 截断不符: %d runes", len([]rune(got)))
	}
	// 上游不可达 → 脱敏错误
	if _, err := NewClient("", "tok", fakeRepos{}).ListRuns(context.Background()); err == nil {
		t.Fatal("baseURL 空（未配置）应返回错误")
	}
}

// TestClientPollRunFailedErrorMergedIntoMessage 锁 Failed 时对方 error 字段并入 Message
// （2026-10-14 实测：runner 判负原因在 error，message 为空 → StageRun.Error 空文案排除断链）。
func TestClientPollRunFailedErrorMergedIntoMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs/{name}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": r.PathValue("name"), "phase": "Failed", "verdict": "",
			"producedBranch": "agentrun/x", "error": "Runner failed: Codex 未返回结构化结果",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "tok", fakeRepos{})
	st, err := c.PollRun(context.Background(), pipeline.AgentTaskRef{Name: "r-1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != "failed" {
		t.Fatalf("phase = %s, 期望 failed", st.Phase)
	}
	if !strings.Contains(st.Message, "Codex 未返回结构化结果") {
		t.Errorf("Message = %q, 期望含对方 error 原因", st.Message)
	}
}
