package main

// template_bootstrap.go 实现「从模板新建应用」一键端点（冷启动旅程审计 R7）。
//
// 编排五步（cmd/core 层聚合，业务包零改动）：
//  1. 建应用（复用 application handler 逻辑路径：QuotaCheck + Create + OnAppCreate 默认流水线）
//  2. 建内置 Gitea 仓库（AutoInit README）
//  3. seed 模板文件（Dockerfile/index.html，经 Gitea contents API 逐个提交）
//  4. 建服务实体（port=80，服务模型 Phase 1——R1 后 deploy 自动绑定带出 Port）
//  5. 触发首轮 CI run（构建->部署->冒烟全链路）
//
// 失败语义：任一步失败返回已创建的应用 ID + 错误（前端引导用户去对应 tab 继续，
// 不回滚——应用/仓库是有价值的部分成果；仅 gitea seed 失败时流水线仍可手动触发）。

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/pipeline"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/service"
)

// templateApp 定义一个应用模板（KISS：先一个 Hello Web；后续扩列表）。
type templateApp struct {
	Slug string // 模板标识（hello-web）
	Name string // 展示名
	Desc string
	// Files 相对路径 -> 内容（提交到内置仓库 main 分支）。
	Files map[string]string
	// Port 服务端口（同时是 Dockerfile EXPOSE 与容器监听端口）。
	Port int
	// ServiceType 服务模型类型（hello-web 是 static——nginx 静态页）。
	ServiceType string
}

// builtinTemplates 平台预置模板（文件内容自包含，无外部依赖）。
// 基础镜像用占位 {REGISTRY}——seed 时替换为平台镜像仓库（集群内可达）。
var builtinTemplates = []templateApp{{
	Slug: "hello-web",
	Name: "Hello Web",
	Desc: "单页静态网站（nginx）——最快验证平台全链路：构建→部署→冒烟→域名可访问",
	Port: 80, ServiceType: "static",
	Files: map[string]string{
		"Dockerfile": "FROM {REGISTRY}/library/nginx:alpine\nCOPY index.html /usr/share/nginx/html/index.html\nEXPOSE 80\n",
		"index.html": "<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n<title>Hello PaaS</title>\n<style>body{font-family:system-ui;max-width:640px;margin:80px auto;padding:0 20px;color:#333}h1{color:#4f6ef7}</style>\n</head>\n<body>\n<h1>🎉 Hello PaaS</h1>\n<p>你的第一个应用已通过模板部署成功。推送代码到仓库即可持续迭代。</p>\n</body>\n</html>\n",
	},
}}

// templateBootstrapHandler 「从模板新建应用」复合端点。
type templateBootstrapHandler struct {
	apps     *application.Handler // 复用 Create 的配额/hook 路径（经 ServeHTTP 内部调用不可行，直接复用 repo 路径）
	appRepo  application.Repository
	repos    devops.CodeRepoRepository
	svcRepo  service.Repository
	pipes    pipeline.Repository
	gitea       *gitea.Client
	imageReg    string // 模板 Dockerfile 基础镜像 {REGISTRY} 替换值（集群内 registry，builder 可拉）
	coreBaseURL string // core 集群内地址（如 http://paas-core.paas.svc:8080，webhook 回调）
	trigger  func(r *http.Request, appID, pid, branch string) (pipeline.PipelineRun, error)
	allow    func(r *http.Request, perm string) bool
}

// ServeHTTP POST /api/applications/from-template
// body: {templateSlug, name, desc?, repoName?}
// resp: {data: {appId, repoId, serviceId, runId}}
func (h *templateBootstrapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.allow != nil && !h.allow(r, application.PermApplicationWrite) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		TemplateSlug string `json:"templateSlug"`
		Name         string `json:"name"`
		Desc         string `json:"desc"`
		RepoName     string `json:"repoName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	tpl := pickTemplate(body.TemplateSlug)
	if tpl == nil {
		httputil.WriteError(w, http.StatusBadRequest, "未知模板: "+body.TemplateSlug)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "字段非法或缺失: name")
		return
	}
	if h.gitea == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "内置 Git 后端未启用")
		return
	}

	// 1. 建应用（复用 application handler 的 POST 语义——经内部构造请求保持配额/hook 一致）。
	// 注意必须设 URL（handler 内读 r.URL.Path 分发；裸 Request 的 URL 为 nil 会 panic）。
	appReq, _ := json.Marshal(application.Application{Name: strings.TrimSpace(body.Name), Desc: strings.TrimSpace(body.Desc)})
	rec := newInternalRecorder()
	inner, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/api/applications", bytes.NewReader(appReq))
	h.apps.ServeHTTP(rec, inner)
	if rec.code != http.StatusCreated {
		httputil.WriteError(w, rec.code, rec.body)
		return
	}
	var created struct {
		Data application.Application `json:"data"`
	}
	_ = json.Unmarshal([]byte(rec.body), &created)
	app := created.Data

	// 后续任一步失败：应用已建（有价值），返回部分成果 + 错误指引（不回滚）。
	partial := map[string]any{"appId": app.ID}

	// 2. 建内置仓库。
	repoName := body.RepoName
	if repoName == "" {
		repoName = app.ID // 应用 ID 天然小写字母/数字/中划线（app-<ts>），直接作仓库名
	}
	gRepo, err := h.gitea.CreateRepo(r.Context(), gitea.CreateRepoInput{
		Name: repoName, DefaultBranch: "main", Private: true, AutoInit: true,
	})
	if err != nil {
		partial["hint"] = "仓库创建失败（可去「代码仓库」tab 手动绑定）"
		httputil.WriteError(w, http.StatusBadGateway, fmt.Sprintf("%v；%v", err, partial["hint"]))
		return
	}
	// 预生成 repo ID（memory/pg Create 值传递不回传；预置 ID 两实现均直接采用）。
	repo := devops.CodeRepo{
		ID: "repo-" + app.ID[len("app-"):], AppID: app.ID, Source: devops.RepoSourceInternal,
		GiteaOwner: h.gitea.Username(), GiteaRepo: repoName,
		GitURL: gRepo.CloneURL, CloneURL: h.gitea.CloneURLWithAuth(h.gitea.Username(), repoName),
		Branch: gRepo.DefaultBranch,
	}
	if err := h.repos.CreateRepo(r.Context(), repo); err != nil {
		partial["hint"] = "仓库记录写入失败（Gitea 仓库已建）"
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	partial["repoId"] = repo.ID

	// 3. seed 模板文件（best-effort——失败不阻断后续，用户可手动推文件）。
	owner := h.gitea.Username()
	i := 0
	for path, content := range tpl.Files {
		i++
		content = strings.ReplaceAll(content, "{REGISTRY}", h.imageReg)
		if err := h.gitea.CreateFile(r.Context(), owner, repoName, path,
			fmt.Sprintf("seed from template %s (%d/%d: %s)", tpl.Slug, i, len(tpl.Files), path),
			base64.StdEncoding.EncodeToString([]byte(content))); err != nil {
			log.Printf("模板 seed 文件失败（不阻断）: app=%s file=%s: %v", app.ID, path, err)
			partial["seeded"] = false
		}
	}
	if partial["seeded"] == nil {
		partial["seeded"] = true
	}

	// 4. 建服务实体（R1 后 deploy 自动绑定带出 Port）。Create 返回 error、ID 内部生成，Create 后按名查回。
	if err := h.svcRepo.Create(r.Context(), service.Service{
		AppID: app.ID, Name: "web", Type: tpl.ServiceType, Port: tpl.Port, Replicas: 1,
	}); err != nil {
		partial["hint"] = "服务创建失败（可去「服务」tab 手动创建后点「部署」）"
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	if svcs, lErr := h.svcRepo.List(r.Context(), app.ID); lErr == nil {
		for _, s := range svcs {
			if s.Name == "web" {
				partial["serviceId"] = s.ID
				break
			}
		}
	}

	// 5. CI pipeline 升级 webhook 触发 + Gitea 注册 webhook（push 即自动构建部署——
	//    日常迭代旅程核心：改代码 push 后零操作上线）。best-effort，失败保持 manual（可手动运行）。
	pipes, err := h.pipes.ListPipelines(r.Context(), app.ID)
	if err == nil {
		for i, p := range pipes {
			if p.Kind != pipeline.KindCI {
				continue
			}
			if p.Trigger.Type != pipeline.TriggerWebhook {
				p.Trigger = pipeline.PipelineTrigger{Type: pipeline.TriggerWebhook, Branch: "main"}
				if p.Trigger.Token == "" {
					// 与 pipeline handler normalizeTrigger 同款生成（私有函数不可复用，此处最小重复）
					buf := make([]byte, 32)
					if _, e := rand.Read(buf); e == nil {
						p.Trigger.Token = hex.EncodeToString(buf)
					}
				}
				if _, uErr := h.pipes.UpdatePipeline(r.Context(), p); uErr != nil {
					log.Printf("CI 升级 webhook 触发失败（保持 manual）: app=%s: %v", app.ID, uErr)
				} else {
					pipes[i] = p
				}
			}
			// 注册 Gitea webhook（core 集群内地址回调；422 同 URL 已存在幂等跳过）
			if h.coreBaseURL != "" && p.Trigger.Token != "" {
				hookURL := fmt.Sprintf("%s/api/webhooks/pipeline/%s?token=%s", h.coreBaseURL, p.ID, p.Trigger.Token)
				if wErr := h.gitea.CreateWebhook(r.Context(), owner, repoName, hookURL); wErr != nil {
					log.Printf("Gitea webhook 注册失败（不阻断，push 自动触发暂不可用）: app=%s: %v", app.ID, wErr)
					partial["hint"] = "push 自动触发未配置成功（可去「流水线」tab 手动运行）"
				}
			}
			// 首轮手动触发（webhook 只管后续 push）
			run, trErr := h.trigger(r, app.ID, p.ID, "main")
			if trErr != nil {
				log.Printf("模板首轮 CI 触发失败（不阻断）: app=%s: %v", app.ID, trErr)
				partial["hint"] = "首轮构建触发失败（可去「流水线」tab 手动运行）"
			} else {
				partial["runId"] = run.ID
			}
			break
		}
	}

	httputil.WriteDataCreated(w, partial)
}

// internalRecorder 捕获内部调 application handler 的响应（status/body），
// 复用其 Create 语义（配额拦截 + OnAppCreate 默认流水线 hook）避免逻辑两处漂移。
type internalRecorder struct {
	code int
	body string
}

func (r *internalRecorder) Header() http.Header { return http.Header{} }
func (r *internalRecorder) Write(b []byte) (int, error) {
	r.body += string(b)
	return len(b), nil
}
// WriteHeader 保留首个显式状态码（真实 server 语义：二次 WriteHeader no-op；
// application Create 先 201 再经 WriteData 写 200，首个 201 才是真实状态）。
func (r *internalRecorder) WriteHeader(code int) {
	if r.code == http.StatusOK {
		r.code = code
	}
}

func newInternalRecorder() *internalRecorder { return &internalRecorder{code: http.StatusOK} }

func pickTemplate(slug string) *templateApp {
	for i := range builtinTemplates {
		if builtinTemplates[i].Slug == slug {
			return &builtinTemplates[i]
		}
	}
	return nil
}
