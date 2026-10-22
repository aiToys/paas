package skill_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/skill/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func call(h http.Handler, tid, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(tenant.WithTenant(context.Background(), tid))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) skill.Skill {
	t.Helper()
	var data struct {
		Data skill.Skill `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data.Data
}

func TestCreateEnabledDefaultAndExplicitFalse(t *testing.T) {
	h := skill.NewHandler(memory.NewStore())
	for _, tc := range []struct {
		body    string
		enabled bool
	}{
		{`{"name":"default","instructions":"x","installedFrom":"builtin:forged"}`, true},
		{`{"name":"disabled","instructions":"x","enabled":false}`, false},
	} {
		w := call(h, "a", "POST", "/api/skills", tc.body)
		if w.Code != 201 {
			t.Fatalf("创建失败：%d %s", w.Code, w.Body.String())
		}
		s := decode(t, w)
		if s.Enabled != tc.enabled || s.InstalledFrom != "" {
			t.Fatalf("状态或来源错误：%+v", s)
		}
	}
}

func TestReferencedSkillDeletionAndDisablingAreRejected(t *testing.T) {
	repo := memory.NewStore()
	s, _ := repo.Create(tenant.WithTenant(context.Background(), "a"), skill.Skill{Name: "used", Instructions: "x", Enabled: true})
	h := skill.NewHandler(repo)
	h.References = func(context.Context, string) ([]skill.Reference, error) {
		return []skill.Reference{{ID: "agent-a", Name: "客服"}}, nil
	}
	for _, tc := range []struct{ method, path, body string }{
		{"DELETE", "/api/skills/" + s.ID, ""},
		{"PUT", "/api/skills/" + s.ID, `{"name":"used","instructions":"x","enabled":false}`},
	} {
		if w := call(h, "a", tc.method, tc.path, tc.body); w.Code != 409 {
			t.Fatalf("应阻断引用变更：%d %s", w.Code, w.Body.String())
		}
	}
	if w := call(h, "b", "GET", "/api/skills/"+s.ID+"/references", ""); w.Code != 404 {
		t.Fatalf("跨租户引用不应泄漏：%d", w.Code)
	}
	h.References = func(context.Context, string) ([]skill.Reference, error) { return nil, errors.New("backend down") }
	if w := call(h, "a", "DELETE", "/api/skills/"+s.ID, ""); w.Code != 503 {
		t.Fatalf("引用未知应拒绝：%d", w.Code)
	}
	h.References = func(context.Context, string) ([]skill.Reference, error) { return nil, nil }
	if w := call(h, "a", "DELETE", "/api/skills/"+s.ID, ""); w.Code != 200 {
		t.Fatalf("解绑后应可删除：%d", w.Code)
	}
}

func TestBuiltinInstallIsTenantIsolatedAndPreservesEdits(t *testing.T) {
	repo := memory.NewStore()
	h := skill.NewHandler(repo)
	path := "/api/skills/templates/structured-answer/install"
	w := call(h, "a", "POST", path, "")
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	a := decode(t, w)
	if !a.Enabled || !strings.HasPrefix(a.InstalledFrom, "builtin:structured-answer@1.0.0#") {
		t.Fatalf("模板来源不完整：%+v", a)
	}
	a.Instructions = "用户修改的版本"
	repo.Update(tenant.WithTenant(context.Background(), "a"), a)
	w = call(h, "a", "POST", path, "")
	if w.Code != 200 || decode(t, w).Instructions != a.Instructions {
		t.Fatalf("重装覆盖用户编辑：%s", w.Body.String())
	}
	b := decode(t, call(h, "b", "POST", path, ""))
	if b.ID == a.ID || b.Instructions == a.Instructions {
		t.Fatal("模板副本没有隔离")
	}
	list, _ := repo.List(tenant.WithTenant(context.Background(), "a"))
	if len(list) != 1 {
		t.Fatal("重复安装产生多份")
	}
	if w := call(h, "a", "POST", "/api/skills/templates/onepaas-app-delivery/install", ""); w.Code != 400 {
		t.Fatal("不应把外部技能作为平台指令安装")
	}
	h.Authorize = func(*http.Request, string) bool { return false }
	if w := call(h, "a", "POST", path, ""); w.Code != 403 {
		t.Fatal("安装未检查权限")
	}
	h.Authorize = func(_ *http.Request, perm string) bool { return perm == skill.PermSkillWrite }
	if w := call(h, "a", "POST", path, ""); w.Code != 200 {
		t.Fatal("安装应遵循声明的 agent:write 权限，不隐含额外读取权限")
	}
	if w := call(h, "a", "GET", "/api/skills/templates", ""); w.Code != 403 {
		t.Fatal("读取目录仍需 agent:read")
	}
}

func TestConcurrentTemplateInstallAndStandardDownload(t *testing.T) {
	repo := memory.NewStore()
	h := skill.NewHandler(repo)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call(h, "a", "POST", "/api/skills/templates/knowledge-answer/install", "")
			if w.Code != 200 && w.Code != 201 {
				t.Error(w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	list, _ := repo.List(tenant.WithTenant(context.Background(), "a"))
	if len(list) != 1 {
		t.Fatalf("并发安装数=%d", len(list))
	}
	templates, err := skill.Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range templates {
		w := call(h, "a", "GET", "/api/skills/templates/"+tmpl.Slug+"/download", "")
		if w.Code != 200 || w.Body.String() != tmpl.Markdown || len(tmpl.ContentHash) != 64 {
			t.Fatalf("标准内容下载不一致：%s", tmpl.Slug)
		}
	}
}
