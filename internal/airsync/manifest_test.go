package airsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "paas-0.1.0.tgz"), []byte("fake chart"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core-0.1.0.tar"), []byte("fake image tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	images := []ImageRef{{Name: "ghcr.io/aitoys/paas-core", Tag: "0.1.0", File: "core-0.1.0.tar"}}
	m, err := BuildManifest(dir, "0.1.0", "0.1.0", "paas-0.1.0.tgz", images, "2026-07-30T00:00:00Z")
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("应含 2 文件 sha256，实际 %d", len(m.Files))
	}
	_, mismatch, err := VerifyManifest(dir)
	if err != nil || len(mismatch) != 0 {
		t.Fatalf("未篡改应通过，mismatch=%v err=%v", mismatch, err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "core-0.1.0.tar")
	if err := os.WriteFile(f, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildManifest(dir, "0.1.0", "0.1.0", "", []ImageRef{{File: "core-0.1.0.tar"}}, ""); err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	// 篡改文件
	if err := os.WriteFile(f, []byte("TAMPERED"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mismatch, _ := VerifyManifest(dir)
	if len(mismatch) == 0 {
		t.Fatalf("篡改后应检出不一致")
	}
}

func TestVerifyDetectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "core.tar")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = BuildManifest(dir, "0.1.0", "0.1.0", "", []ImageRef{{File: "core.tar"}}, "")
	_ = os.Remove(f)
	_, mismatch, _ := VerifyManifest(dir)
	if len(mismatch) == 0 {
		t.Fatalf("文件缺失应检出")
	}
}
