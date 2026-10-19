package backup

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRestoreRetryReusesOperationAndRejectsParameterChange(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-snap"] = Backup{ID: "bk-snap", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot, Status: StatusCompleted,
		ArtifactRef: "file://paas-backups/bk-snap.dump", Checksum: strings.Repeat("a", 64), Engine: "postgres"}
	h := NewHandler(repo, WithExecutor(newFakeExecutor(ProbeResult{}), fakeTargetsForHandler{}, fakeRestorer{}))
	h.Authorize = authWith("dataservice:write")
	request := func(name string) *httptest.ResponseRecorder {
		r := withTenant(httptest.NewRequest(http.MethodPost, "/api/backups/bk-snap/restore", strings.NewReader(fmt.Sprintf(`{"name":%q}`, name))), "t-acme")
		r.Header.Set("Idempotency-Key", "retry-same-operation")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("new-db"); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("new-db"); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("another-db"); w.Code != http.StatusConflict {
		t.Fatal(w.Code, w.Body.String())
	}
	count := 0
	for _, b := range repo.items {
		if b.Mode == ModeRestore {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重试产生了 %d 条恢复操作", count)
	}
}

func TestSuccessfulJobWithoutIntegrityEvidenceDoesNotComplete(t *testing.T) {
	repo := newFakeRepo()
	repo.items["b"] = Backup{ID: "b", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot, Status: StatusRunning, CreatedAt: time.Now()}
	NewPacer(repo, newFakeExecutor(ProbeResult{Phase: "succeeded"}), &fakeTargets{}).Sweep(context.Background())
	if repo.items["b"].Status != StatusFailed || !repo.items["b"].VerifiedAt.IsZero() {
		t.Fatalf("缺证据被判成功: %+v", repo.items["b"])
	}
}

func TestRestoreScriptChecksChecksumBeforeImportAndReadsTables(t *testing.T) {
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("archive")))
	script := jobScript(Backup{ID: "bk-r", Mode: ModeRestore, SourceBackupID: "bk-source", Checksum: sum}, Target{ServiceName: "ds-target"})
	check, restore, read := strings.Index(script, "sha256sum -c"), strings.Index(script, "pg_restore --exit-on-error"), strings.Index(script, "SELECT count(*)")
	if check < 0 || restore <= check || read <= restore || !strings.Contains(script, sum) {
		t.Fatal("恢复必须先校验，再导入，再读取用户表")
	}
	if strings.Contains(script, "sha256=restored") {
		t.Fatal("不得伪造校验和")
	}
}
