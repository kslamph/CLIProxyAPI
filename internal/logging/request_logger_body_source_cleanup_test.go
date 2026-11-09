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
	for _, path := range []string{filepath.Join(stale, "part.tmp"), stale} {
		if errChtimes := os.Chtimes(path, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); errChtimes != nil {
			t.Fatal(errChtimes)
		}
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

func TestCleanupStaleRequestLogPartsKeepsAppendedParts(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	dir := filepath.Join(root, "request-log-parts-appending")
	if errMkdir := os.Mkdir(dir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	part := filepath.Join(dir, "part.tmp")
	if errWrite := os.WriteFile(part, []byte("payload"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	// Directory mtime is old (no entries added), but the part was appended recently.
	if errChtimes := os.Chtimes(part, now.Add(-time.Hour), now.Add(-time.Hour)); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	if errChtimes := os.Chtimes(dir, old, old); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	if errCleanup := cleanupStaleRequestLogPartsAt(root, now, 24*time.Hour); errCleanup != nil {
		t.Fatal(errCleanup)
	}
	if _, errStat := os.Stat(part); errStat != nil {
		t.Fatalf("recently appended part was removed: %v", errStat)
	}
}

func TestCleanupStaleRequestLogPartsKeepsActiveSourceAndRemovesAfterCleanup(t *testing.T) {
	root := t.TempDir()
	source, errSource := NewFileBodySourceInDir(root, "active")
	if errSource != nil {
		t.Fatal(errSource)
	}
	if errAppend := source.AppendBytes([]byte("first")); errAppend != nil {
		t.Fatal(errAppend)
	}
	// Age the whole spool as if the request had been idle for over a day.
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range append(source.Paths(), source.dir) {
		if errChtimes := os.Chtimes(path, old, old); errChtimes != nil {
			t.Fatal(errChtimes)
		}
	}
	other, errOther := NewFileBodySourceInDir(root, "other") // triggers the stale sweep
	if errOther != nil {
		t.Fatal(errOther)
	}
	defer other.Cleanup()
	if errAppend := source.AppendBytes([]byte("second")); errAppend != nil {
		t.Fatal(errAppend)
	}
	data, errBytes := source.Bytes()
	if errBytes != nil || string(data) != "firstsecond" {
		t.Fatalf("active source data = %q, err = %v", data, errBytes)
	}
	if errCleanup := source.Cleanup(); errCleanup != nil {
		t.Fatal(errCleanup)
	}
	if isActiveRequestLogParts(source.dir) {
		t.Fatal("cleaned source is still registered active")
	}
	if errMkdir := os.Mkdir(source.dir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errChtimes := os.Chtimes(source.dir, old, old); errChtimes != nil {
		t.Fatal(errChtimes)
	}
	if errCleanup := cleanupStaleRequestLogPartsAt(root, time.Now(), requestLogPartsMaxAge); errCleanup != nil {
		t.Fatal(errCleanup)
	}
	if _, errStat := os.Stat(source.dir); !os.IsNotExist(errStat) {
		t.Fatalf("inactive stale spool stat error = %v, want not exist", errStat)
	}
}
