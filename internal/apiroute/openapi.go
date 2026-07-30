// Package apiroute 提供路由注册 + OpenAPI 3.0 契约生成的单一真源。
// Registry 同时驱动 Go 1.22 method-scoped mux 注册与 OpenAPI spec 生成，
// 消除"路由声明两处"的漂移；手写 Go→JSON Schema reflector，零外部依赖。
package apiroute

// OpenAPI 3.0 文档结构（最小子集，足以生成合法 spec 与前端 TS 类型）。

// Document 是 OpenAPI 3.0 根文档。
type Document struct {
	OpenAPI    string               `json:"openapi"` // 固定 "3.0.3"
	Info       Info                 `json:"info"`
	Paths      map[string]*PathItem `json:"paths"`
	Components Components           `json:"components,omitempty"`
}

// Info 描述 API 元信息。
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// PathItem 是一个路径下各 HTTP 方法的操作集合。
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
}

// Operation 是单个端点操作。
type Operation struct {
	Summary     string                `json:"summary,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Description string                `json:"description,omitempty"`
	Security    []SecurityRequirement `json:"security,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[int]*Response     `json:"responses"`
}

// SecurityRequirement 是 {方案名: [所需 scope]}。perm 映射为单元素 scope。
type SecurityRequirement map[string][]string

// RequestBody 描述请求体（application/json）。
type RequestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]MediaType `json:"content"`
}

// Response 描述一个状态码的响应。
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType 关联一个 schema。
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Components 收纳可复用 schema 与安全方案。
type Components struct {
	Schemas         map[string]*Schema         `json:"schemas,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

// SecurityScheme 描述 Bearer API Key 鉴权。
type SecurityScheme struct {
	Type        string `json:"type"`   // "http"
	Scheme      string `json:"scheme"` // "bearer"
	Description string `json:"description,omitempty"`
}

// Schema 是 JSON Schema 子集。Ref 非空时其余字段忽略（$ref 指向 components.schemas）。
type Schema struct {
	Type                 string             `json:"type,omitempty"` // object/array/string/integer/number/boolean
	Format               string             `json:"format,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Description          string             `json:"description,omitempty"`
}
