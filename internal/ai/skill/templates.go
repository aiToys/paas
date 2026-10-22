package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
	skillassets "github.com/aitoys/paas/skills"
)

// Template 是官方只读目录项；instructions 仅支持静态指令，external-agent 供外部客户端使用。
type Template struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Category     string `json:"category"`
	UseCases     string `json:"useCases"`
	Examples     string `json:"examples"`
	Version      string `json:"version"`
	Runtime      string `json:"runtime"`
	License      string `json:"license"`
	ContentHash  string `json:"contentHash"`
	Markdown     string `json:"-"`
}

// Templates 只解析仓库自有、受控的单行标量 frontmatter，不是任意 Skill 包的 YAML 导入器。
func Templates() ([]Template, error) {
	raw, err := skillassets.Files.ReadFile("catalog.json")
	if err != nil {
		return nil, err
	}
	var list []Template
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	for i := range list {
		t := &list[i]
		md, err := skillassets.Files.ReadFile(t.Slug + "/SKILL.md")
		if err != nil {
			return nil, err
		}
		header, body, ok := strings.Cut(strings.TrimPrefix(string(md), "---\n"), "\n---\n")
		if !strings.HasPrefix(string(md), "---\n") || !ok {
			return nil, fmt.Errorf("无效内置 Skill：%s", t.Slug)
		}
		fields := map[string]string{}
		for _, line := range strings.Split(header, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
		if fields["name"] != t.Slug || fields["description"] == "" || fields["version"] == "" || (fields["runtime"] != "instructions" && fields["runtime"] != "external-agent") || strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("内置 Skill 元数据不完整：%s", t.Slug)
		}
		t.Description, t.Version, t.Runtime, t.License = fields["description"], fields["version"], fields["runtime"], fields["license"]
		t.Instructions, t.Markdown = strings.TrimSpace(body), string(md)
		hash := sha256.Sum256(md)
		t.ContentHash = hex.EncodeToString(hash[:])
	}
	return list, nil
}

func (h *Handler) serveTemplates(w http.ResponseWriter, r *http.Request, path string) {
	perm := PermSkillRead
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/install") {
		perm = PermSkillWrite
	}
	if !h.allow(w, r, perm) {
		return
	}
	list, err := Templates()
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if path == "/api/skills/templates" {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, 405, "method not allowed")
			return
		}
		httputil.WriteData(w, list)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/skills/templates/"), "/")
	if len(parts) != 2 {
		httputil.WriteError(w, 404, "not found")
		return
	}
	var selected *Template
	for i := range list {
		if list[i].Slug == parts[0] {
			selected = &list[i]
			break
		}
	}
	if selected == nil {
		httputil.WriteError(w, 404, "template not found")
		return
	}
	if parts[1] == "download" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-SKILL.md"`, selected.Slug))
		_, _ = w.Write([]byte(selected.Markdown))
		return
	}
	if parts[1] != "install" || r.Method != http.MethodPost {
		httputil.WriteError(w, 405, "method not allowed")
		return
	}
	if selected.Runtime != "instructions" {
		httputil.WriteError(w, 400, "该技能供外部 Agent 使用，请下载标准文件；平台不执行脚本或平台管理操作")
		return
	}
	tid, ok := tenant.TenantFrom(r.Context())
	if !ok || tid == "" {
		httputil.WriteError(w, 403, "tenant required")
		return
	}
	hash := sha256.Sum256([]byte(tid + "\x00" + selected.Slug))
	id := "skill-builtin-" + hex.EncodeToString(hash[:])
	prefix := "builtin:" + selected.Slug + "@"
	// 稳定 ID 保证重试不制造副本；升级模板也不覆盖用户已编辑的内容。
	if old, err := h.repo.Get(r.Context(), id); err == nil {
		if !strings.HasPrefix(old.InstalledFrom, prefix) {
			h.writeErr(w, ErrSkillExists)
			return
		}
		httputil.WriteData(w, old)
		return
	} else if err != ErrSkillNotFound {
		h.writeErr(w, err)
		return
	}
	saved, err := h.repo.Create(r.Context(), Skill{ID: id, Name: selected.Name, Description: selected.Description, Instructions: selected.Instructions, Category: selected.Category, UseCases: selected.UseCases, Examples: selected.Examples, Enabled: true, InstalledFrom: prefix + selected.Version + "#" + selected.ContentHash})
	if err == ErrSkillExists {
		if old, getErr := h.repo.Get(r.Context(), id); getErr == nil && strings.HasPrefix(old.InstalledFrom, prefix) {
			httputil.WriteData(w, old)
			return
		}
	}
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteDataCreated(w, saved)
}
