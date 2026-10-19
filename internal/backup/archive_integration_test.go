//go:build integration

package backup

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// 仅接受明确指定的隔离容器；不读取用户的数据库连接或 kubeconfig。
func TestArchiveRestoreIntegrityAndReadback(t *testing.T) {
	container := os.Getenv("PAAS_BACKUP_TEST_CONTAINER")
	if !strings.HasPrefix(container, "paas-console-fix-") {
		t.Skip("未指定隔离 PostgreSQL 容器")
	}
	run := func(db, script string) (string, error) {
		cmd := exec.Command("docker", "exec", "-i", "-e", "PGUSER=postgres", "-e", "PGPASSWORD=paas-test-only", "-e", "PGDATABASE="+db, container, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	setup := `set -e
psql -X -v ON_ERROR_STOP=1 -c 'DROP DATABASE IF EXISTS paas_source WITH (FORCE)' -c 'DROP DATABASE IF EXISTS paas_restored WITH (FORCE)'
psql -X -v ON_ERROR_STOP=1 -c 'CREATE DATABASE paas_source' -c 'CREATE DATABASE paas_restored'
`
	if out, err := run("postgres", setup); err != nil {
		t.Fatal(err, out)
	}
	t.Cleanup(func() {
		_, _ = run("postgres", `psql -c 'DROP DATABASE IF EXISTS paas_source WITH (FORCE)' -c 'DROP DATABASE IF EXISTS paas_restored WITH (FORCE)'`)
	})
	if out, err := run("paas_source", `psql -X -v ON_ERROR_STOP=1 -c "CREATE TABLE docs(id int PRIMARY KEY, body text); INSERT INTO docs VALUES (1,'你好'),(2,'restore-check');"`); err != nil {
		t.Fatal(err, out)
	}
	b := Backup{ID: "bk-fixture", Mode: ModeSnapshot}
	out, err := run("paas_source", jobScript(b, Target{ServiceName: "localhost"}))
	if err != nil {
		t.Fatal(err, out)
	}
	var checksum string
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "sha256=") {
			checksum = strings.TrimPrefix(field, "sha256=")
		}
	}
	if !ValidChecksum(checksum) {
		t.Fatalf("未取得真实归档校验和: %s", out)
	}
	r := Backup{ID: "bk-restore", Mode: ModeRestore, SourceBackupID: b.ID, Checksum: checksum}
	if out, err = run("postgres", "cp /backups/bk-fixture.dump /backups/good.dump; printf corruption >> /backups/bk-fixture.dump"); err != nil {
		t.Fatal(err, out)
	}
	if out, err = run("paas_restored", jobScript(r, Target{ServiceName: "localhost"})); err == nil {
		t.Fatalf("损坏归档未被拒绝: %s", out)
	}
	if out, err = run("paas_restored", `psql -X -tA -c "SELECT count(*) FROM pg_tables WHERE schemaname='public'"`); err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("校验失败仍写入目标: %v %s", err, out)
	}
	if out, err = run("postgres", "cp /backups/good.dump /backups/bk-fixture.dump"); err != nil {
		t.Fatal(err, out)
	}
	if out, err = run("paas_restored", jobScript(r, Target{ServiceName: "localhost"})); err != nil {
		t.Fatal(err, out)
	}
	if out, err = run("paas_restored", `psql -X -tA -c "SELECT id || ':' || body FROM docs ORDER BY id"`); err != nil || strings.TrimSpace(out) != "1:你好\n2:restore-check" {
		t.Fatalf("恢复数据读回不一致: %v %s", err, out)
	}
}
