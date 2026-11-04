package logging

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCleanupStaleRequestLogPartsAgeAndSymlinkPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test requires platform support")
	}
	root := t.TempDir()
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	stale := filepath.Join(root, "request-log-parts-stale")
	fresh := filepath.Join(root, "request-log-parts-fresh")
	link := filepath.Join(root, "request-log-parts-link")
	for _, dir := range []string{stale, fresh} {
		if errMkdir := os.Mkdir(dir, 0o755); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		if errWrite := os.WriteFile(filepath.Join(dir, "part.tmp"), []byte("payload"), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errChtimes := os.Chtimes(stale, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	if errChtimes := os.Chtimes(fresh, now.Add(-23*time.Hour), now.Add(-23*time.Hour)); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	if errSymlink := os.Symlink(stale, link); errSymlink != nil {
		t.Fatal(errSymlink)
	}
	if errCleanup := cleanupStaleRequestLogPartsAt(root, now, 24*time.Hour); errCleanup != nil {
		t.Fatalf("cleanupStaleRequestLogPartsAt() error = %v", errCleanup)
	}
	if _, errStat := os.Stat(stale); !os.IsNotExist(errStat) {
		t.Fatalf("stale spool stat error = %v, want not exist", errStat)
	}
	if _, errStat := os.Stat(fresh); errStat != nil {
		t.Fatalf("fresh spool stat error = %v", errStat)
	}
	if _, errStat := os.Lstat(link); errStat != nil {
		t.Fatalf("symlink stat error = %v", errStat)
	}
	if target, errEval := os.Readlink(link); errEval != nil || target != stale {
		t.Fatalf("symlink target = %q, error = %v", target, errEval)
	}
}

func TestNewFileBodySourceInDirCleansStaleSpools(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "request-log-parts-old")
	if errMkdir := os.Mkdir(stale, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	old := time.Now().Add(-48 * time.Hour)
	if errChtimes := os.Chtimes(stale, old, old); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	source, errSource := NewFileBodySourceInDir(root, "test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir() error = %v", errSource)
	}
	defer source.Cleanup()
	if _, errStat := os.Stat(stale); !os.IsNotExist(errStat) {
		t.Fatalf("stale spool stat error = %v, want not exist", errStat)
	}
}
