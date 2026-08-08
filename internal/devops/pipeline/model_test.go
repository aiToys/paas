package pipeline

import "testing"

func TestPipelineValidate(t *testing.T) {
	cases := []struct {
		name    string
		p       Pipeline
		wantErr bool
	}{
		{"ok", Pipeline{Name: "p", AppID: "a", Kind: KindCI, TemplateID: "tpl-x"}, false},
		{"emptyTemplateID", Pipeline{Name: "p", AppID: "a", Kind: KindCI}, true}, // 绑定模型：TemplateID 必填
		{"badKind", Pipeline{Name: "p", AppID: "a", Kind: "x", TemplateID: "tpl-x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestStageDefValidate 单独校验 StageDef（绑定模型下 Pipeline.Validate 不再调用它）。
func TestStageDefValidate(t *testing.T) {
	if err := (StageDef{Name: "b", Type: StageBuild}).validate(); err != nil {
		t.Fatalf("valid stage got %v", err)
	}
	if err := (StageDef{Name: "b", Type: "x"}).validate(); err != ErrInvalidStageType {
		t.Fatalf("bad type want ErrInvalidStageType got %v", err)
	}
	if err := (StageDef{Type: StageBuild}).validate(); err != errStageNameRequired {
		t.Fatalf("missing name want errStageNameRequired got %v", err)
	}
}

func TestStageDefValidateAcceptsRelease(t *testing.T) {
	s := StageDef{Name: "发布版本", Type: StageRelease}
	if err := s.validate(); err != nil {
		t.Errorf("release stage 应合法: %v", err)
	}
}

func TestStageRunLogField(t *testing.T) {
	sr := StageRun{Index: 0, Type: StageBuild, Name: "构建", Log: "构建已提交\n"}
	if sr.Log != "构建已提交\n" {
		t.Error("StageRun.Log 字段未生效")
	}
}
