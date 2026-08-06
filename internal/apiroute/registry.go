package apiroute

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
)

// Route 是单个端点操作的元数据。
type Route struct {
	Method      string
	Path        string
	Summary     string
	Tags        []string
	Description string
	Perm        string       // 映射为 BearerAPIKey 的 scope
	RequestBody reflect.Type // 请求体载荷类型；nil = 无
	Response    reflect.Type // 响应载荷类型（不含 {data:} 包裹）；nil = 无 schema
}

// Opt 配置 Route。
type Opt func(*Route)

// Summary 设置操作摘要。
func Summary(s string) Opt { return func(r *Route) { r.Summary = s } }

// Tags 设置分组标签。
func Tags(tags ...string) Opt { return func(r *Route) { r.Tags = tags } }

// Description 设置详细说明。
func Description(s string) Opt { return func(r *Route) { r.Description = s } }

// Perm 设置所需权限（映射为 security scope）。
func Perm(p string) Opt { return func(r *Route) { r.Perm = p } }

// WithReqBody 标记请求体类型（传一个该类型的值，反射取 Type）。
func WithReqBody(sample any) Opt {
	return func(r *Route) { r.RequestBody = reflect.TypeOf(sample) }
}

// WithResp 标记响应载荷类型（list 端点传 []Entity 样本，详情传 Entity 样本）。
// 生成时自动包成 {data: <T>, error: string} 包裹。
func WithResp(sample any) Opt {
	return func(r *Route) { r.Response = reflect.TypeOf(sample) }
}

// BearerScheme 是 API Key Bearer 鉴权方案名（文档内固定）。
const BearerScheme = "BearerAPIKey"

// Registry 是路由 + OpenAPI 元数据的单一真源，own 底层 *http.ServeMux。
type Registry struct {
	mux     *http.ServeMux
	info    Info
	routes  []Route
	schemas map[string]*Schema // 命名类型 → component schema
}

// New 创建 Registry，own 传入的 mux（Register 会向其注册路由）。
func New(mux *http.ServeMux, info Info) *Registry {
	if info.Title == "" {
		info.Title = "API"
	}
	if info.Version == "" {
		info.Version = "0.0.0"
	}
	return &Registry{
		mux:     mux,
		info:    info,
		schemas: map[string]*Schema{},
	}
}

// Mux 返回底层 ServeMux（供 composite 等需要直接注册的场景）。
func (r *Registry) Mux() *http.ServeMux { return r.mux }

// Register 既注册 mux（Go 1.22 method-scoped pattern）又记录 spec 元数据。
// 用于"一个 handler 对应一个端点"的普通路由。
func (r *Registry) Register(method, path string, h http.Handler, opts ...Opt) {
	r.record(method, path, opts...)
	// Go 1.22 method-scoped："GET /api/applications"。
	r.mux.Handle(method+" "+path, h)
}

// Operation 仅记录 spec 元数据，不注册 mux。
// 用于 composite 路由：mux 注册是粗粒度 subtree（直接 mux.Handle），
// 内部派发多个逻辑操作；每个逻辑操作用 Operation 登记，spec 才完整。
func (r *Registry) Operation(method, path string, opts ...Opt) {
	r.record(method, path, opts...)
}

// record 收集 Route 元数据，返回该 Route（供 Register 复用）。
func (r *Registry) record(method, path string, opts ...Opt) Route {
	rt := Route{Method: method, Path: path}
	for _, o := range opts {
		o(&rt)
	}
	r.routes = append(r.routes, rt)
	return rt
}

// Document 组装完整 OpenAPI 3.0 文档（注册过的 schema + 路径 + 安全方案）。
func (r *Registry) Document() *Document {
	// 先解析所有 route 涉及的 schema（填充 r.schemas）。
	paths := map[string]*PathItem{}
	for _, rt := range r.routes {
		item := paths[rt.Path]
		if item == nil {
			item = &PathItem{}
			paths[rt.Path] = item
		}
		op := r.buildOperation(rt)
		switch rt.Method {
		case http.MethodGet:
			item.Get = op
		case http.MethodPost:
			item.Post = op
		case http.MethodPut:
			item.Put = op
		case http.MethodDelete:
			item.Delete = op
		case http.MethodPatch:
			item.Patch = op
		}
	}
	return &Document{
		OpenAPI: "3.0.3",
		Info:    r.info,
		Paths:   paths,
		Components: Components{
			Schemas: r.schemas,
			SecuritySchemes: map[string]*SecurityScheme{
				BearerScheme: {
					Type:        "http",
					Scheme:      "bearer",
					Description: "Authorization: Bearer <api-key>",
				},
			},
		},
	}
}

// buildOperation 把 Route 转为 OpenAPI Operation（含 security、请求体、响应包裹）。
func (r *Registry) buildOperation(rt Route) *Operation {
	op := &Operation{
		Summary:     rt.Summary,
		Tags:        rt.Tags,
		Description: rt.Description,
		Responses:   map[int]*Response{},
	}
	if rt.Perm != "" {
		op.Security = []SecurityRequirement{{BearerScheme: {rt.Perm}}}
	}
	if rt.RequestBody != nil {
		op.RequestBody = &RequestBody{
			Required: true,
			Content: map[string]MediaType{
				"application/json": {Schema: r.schemaOf(rt.RequestBody)},
			},
		}
	}
	// 成功响应：200，载荷包成 {data: T, error: string}。
	if rt.Response != nil {
		op.Responses[200] = &Response{
			Description: "成功",
			Content: map[string]MediaType{
				"application/json": {Schema: r.envelope(rt.Response)},
			},
		}
	} else {
		op.Responses[200] = &Response{Description: "成功"}
	}
	// 统一错误响应。
	op.Responses[429] = errResponse("配额超限或限流")
	op.Responses[500] = errResponse("服务器错误")
	return op
}

// envelope 把载荷 schema inline 包成 {data: <T>, error: string}。
func (r *Registry) envelope(payload reflect.Type) *Schema {
	return &Schema{
		Type: "object",
		Properties: map[string]*Schema{
			"data":  r.schemaOf(payload),
			"error": str(),
		},
	}
}

// errSchema 是 {error: string} 的复用 schema。
var errSchema = &Schema{
	Type: "object",
	Properties: map[string]*Schema{
		"error": str(),
	},
}

// str 是便捷的 string schema 字面量。
func str() *Schema { return &Schema{Type: "string"} }

// errResponse 构造 {error: string} 响应。
func errResponse(desc string) *Response {
	return &Response{
		Description: desc,
		Content: map[string]MediaType{
			"application/json": {Schema: errSchema},
		},
	}
}

// ServeSpec 返回 /openapi.json handler（公开，无鉴权）。
func ServeSpec(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		doc := reg.Document()
		// 路径按字典序输出，spec 稳定（便于 diff/缓存）。
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc)
	})
}

// SortedSchemaNames 返回已登记 schema 名（按字典序），供测试/调试。
func (r *Registry) SortedSchemaNames() []string {
	names := make([]string, 0, len(r.schemas))
	for n := range r.schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RegisteredPaths 返回去重并按字典序排序的已注册 path 列表。
// 供 metrics middleware 做 route 归一化（最长前缀匹配，把实际 path 映射回带 {id} 的模板）。
func (r *Registry) RegisteredPaths() []string {
	seen := map[string]struct{}{}
	for _, rt := range r.routes {
		seen[rt.Path] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
