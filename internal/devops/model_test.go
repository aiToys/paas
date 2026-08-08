package devops

import "testing"

func TestReleaseLaneAndSourceRunFields(t *testing.T) {
	r := Release{LaneID: "feature-x", SourceRunID: "run-abc"}
	if r.LaneID != "feature-x" || r.SourceRunID != "run-abc" {
		t.Error("Release 新字段未生效")
	}
}
