package pipeline

import "testing"

func TestPipelineValidate(t *testing.T) {
	cases := []struct {
		name    string
		p       Pipeline
		wantErr bool
	}{
		{"ok", Pipeline{Name: "p", AppID: "a", Kind: KindCI, Stages: []StageDef{{Name: "build", Type: StageBuild}}}, false},
		{"emptyStages", Pipeline{Name: "p", AppID: "a", Kind: KindCI}, true},
		{"badKind", Pipeline{Name: "p", AppID: "a", Kind: "x", Stages: []StageDef{{Name: "b", Type: StageBuild}}}, true},
		{"badStageType", Pipeline{Name: "p", AppID: "a", Kind: KindCI, Stages: []StageDef{{Name: "b", Type: "x"}}}, true},
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
