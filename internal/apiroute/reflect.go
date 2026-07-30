package apiroute

import (
	"reflect"
	"strings"
	"time"
)

// schemaOf 把 Go 类型转为 JSON Schema。命名 struct 登记进 components.schemas 并返回 $ref（去重）；
// 匿名类型 inline。nil 安全（空 interface 返回无约束 schema）。
func (r *Registry) schemaOf(t reflect.Type) *Schema {
	if t == nil {
		return &Schema{}
	}
	// 解指针（指针与所指同 schema）。
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// time.Time → string/date-time。
	if t == reflect.TypeOf(time.Time{}) {
		return &Schema{Type: "string", Format: "date-time"}
	}
	// 命名 struct：登记 component，返回 $ref（支持去重与自引用）。
	if t.Name() != "" && t.Kind() == reflect.Struct {
		name := t.Name()
		if _, exists := r.schemas[name]; exists {
			return &Schema{Ref: "#/components/schemas/" + name}
		}
		r.schemas[name] = nil // 占位，打断循环引用
		r.schemas[name] = r.buildStruct(t)
		return &Schema{Ref: "#/components/schemas/" + name}
	}
	return r.buildAnon(t)
}

// buildAnon 处理非命名类型（基本类型、slice、map、匿名 struct）。
func (r *Registry) buildAnon(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.Struct:
		return r.buildStruct(t)
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: r.schemaOf(t.Elem())}
	case reflect.Map:
		// map[string]V → object + additionalProperties: schemaOf(V)。
		return &Schema{Type: "object", AdditionalProperties: r.schemaOf(t.Elem())}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.Interface:
		return &Schema{} // any：无类型约束
	default:
		return &Schema{}
	}
}

// buildStruct 把 struct 字段转 properties。读 json tag：名 / omitempty / "-"。
// 匿名字段（embedded）inline 合并；非导出字段跳过。
func (r *Registry) buildStruct(t reflect.Type) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, opts := parseJSONTag(f.Tag.Get("json"))
		if tag == "-" {
			continue
		}
		// 匿名字段（embedded）且无 json tag 名 → inline 展开其字段（匹配 json 扁平化语义）。
		// 直接 buildStruct（不登记 component / 不 $ref），把属性并入外层。
		if f.Anonymous && tag == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				embed := r.buildStruct(ft)
				for k, v := range embed.Properties {
					s.Properties[k] = v
				}
				s.Required = append(s.Required, embed.Required...)
				continue
			}
		}
		if tag == "" {
			tag = f.Name
		}
		s.Properties[tag] = r.schemaOf(f.Type)
		if !opts.Contains("omitempty") {
			s.Required = append(s.Required, tag)
		}
	}
	return s
}

// tagOptions 是 json tag 选项集合。
type tagOptions []string

func (o tagOptions) Contains(opt string) bool {
	for _, x := range o {
		if x == opt {
			return true
		}
	}
	return false
}

// parseJSONTag 解析 `json:"name,omitempty"` → (name, [omitempty])。
func parseJSONTag(tag string) (name string, opts tagOptions) {
	if tag == "" {
		return "", nil
	}
	parts := strings.Split(tag, ",")
	name = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		opts = parts[1:]
	}
	return
}
