# OpenAPI /docs 交互文档 + 契约完整设计

**日期**：2026-07-30
**状态**：待评审
**关联**：`2026-07-29-openapi-contract-design.md`（route registry 单一真源已落地）、`internal/apiroute/`

## 背景与动机

OpenAPI 3.0 契约已落地（`GET /openapi.json`，55 paths / 75 ops / 33 schemas，route registry 单一真源 + 手写 reflector + 前端 TS 生成）。两个收尾缺口：

1. **无交互文档 UI**——开源用户/集成方只能读裸 JSON，体验差、上手慢。开源 API 项目标配 `/docs`（Swagger UI / Scalar / Redoc）。
2. **部分写操作请求体未登记**——CLAUDE.md 已标注「部分写操作的匿名请求体未登记」，`POST/PUT` 端点的 requestBody schema 不全，契约不完整（前端/SDK 生成缺请求体类型）。

## 范围

**做**：
- `GET /docs` 交互文档端点（Scalar，嵌入式 HTML）。
- 请求体 schema 全覆盖（所有 `POST/PUT` 补 `WithReqBody`）。

**不做（YAGNI）**：自动 mock server、多语言 SDK 生成、Redoc 多版本对比、OAuth/token 试 API（API Key Bearer 已支持，够）。

## 设计

### /docs 端点（Scalar）

`internal/apiroute/` 新增 `docs.go`：`ServeDocs(specURL, title string) http.Handler` 返回 Scalar HTML 页面，页面内 `<script>` 指向 `specURL`（即 `/openapi.json`）。

```go
// ServeDocs 返回 Scalar API 文档 HTML（嵌入式，前端拉 /openapi.json 渲染）。
func ServeDocs(specURL, title string) http.Handler {
    html := fmt.Sprintf(scalarTemplate, specURL, title)
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/html; charset=utf-8")
        io.WriteString(w, html)
    })
}

const scalarTemplate = `<!html>...Scalar CDN script，config.url=%s, title=%s...`
```

`cmd/core/main.go` 注册：`mux.Handle("/docs", apiroute.ServeDocs("/openapi.json", "PaaS API"))`。

**UI 选型：Scalar**（Apache 2.0，现代美观、暗黑模式、内置 try-it-out、响应预览），优于 Swagger UI（老旧）和 Redoc（无 try-it）。用 CDN（`<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference">`），简单；离线场景后续可 vendored 本地 JS（留后续，不阻塞）。

> **无鉴权**：`/docs` 与 `/openapi.json` 一样公开（无鉴权），是契约文档非敏感数据。

### 请求体 schema 全覆盖

盘点所有 `POST/PUT` 端点，补 `apiroute.WithReqBody(请求体类型)`：

| 端点 | 请求体类型 | 现状 |
|------|----------|------|
| `POST /api/applications` | `application.Application` | 补 |
| `POST /api/applications/{id}/bindings` | binding body `{type,name}` | 补（匿名 struct 或 inline） |
| `POST/PUT /api/workloads`、`PUT /api/workloads/{id}` | `workload.Workload` / 扩缩容 body | 补 |
| `POST /api/applications/{id}/{repositories,buildruns,releases}` | devops 各 Input struct | 补 |
| `POST /api/environments` | `environment.Environment` | 补 |
| `POST /api/dataservices` | `dataservice.DataService` | 补 |
| `POST/PUT /api/services`、`POST /api/services/{id}/instances` | governance 各 struct | 补 |
| `POST/PUT /api/routes`、`/api/breakers` | `Route` / `CircuitBreaker` | 补 |
| `POST /api/configcenter/...` | namespace/item/publish body | 补 |
| `POST /api/security/secrets` | `security.Secret` | 补 |
| `PUT /api/billing/quota`、`POST /api/billing/records/generate` | quota / period body | 补 |
| `POST /api/applications/{id}/configs` | `appconfig.ConfigItem` | 补 |

reflector 已支持匿名 struct inline + 命名类型 `$ref`（`buildAnon` + 去重），直接 `WithReqBody(T)` 即可。补齐后 schema 数从 33 增至 ~45（新增请求体命名类型）。

### composite 路由的请求体

composite handler（如 `/api/applications/{id}/{repositories|buildruns|...}`）的各子操作请求体用 `Operation(method, path, WithReqBody(T))` 登记（spec-only，不注册 mux，与现有 composite 模式一致）。

## 验收标准

1. `GET /docs` 返回 HTML，浏览器打开显示 Scalar 文档，可浏览全部 75 操作 + try-it-out（填 API Key 试请求）。
2. `GET /openapi.json` 的所有 `POST/PUT` 操作含 `requestBody`（`content.application/json.schema`），无遗漏。
3. 前端 `pnpm gen:api` 重新生成，`types.gen.ts` 含请求体类型（paths[x].post.requestBody）。
4. `/docs` 与 `/openapi.json` 公开无鉴权；Scalar CDN 加载正常（离线降级提示留后续）。
5. license：Scalar Apache 2.0（CDN），无新增 Go 依赖。

## 风险与对策

- **CDN 依赖**：Scalar 走 jsdelivr CDN，离线/内网环境加载失败。对策：HTML 内加 `<noscript>`/加载失败提示 + 文档注明可 vendored（后续切片把 JS 本地化）。
- **请求体类型未导出**：若某些 handler 用未导出 struct 作请求体，reflector 会跳过（json tag 不导出）。对策：盘点时确认请求体类型导出，必要时提升导出或用 inline 匿名 struct。
- **composite 子操作 path 参数化**：`/api/applications/{id}/releases` 的 Operation path 需与 mux 注册的 composite prefix 一致，避免 spec 与实际路由漂移。
