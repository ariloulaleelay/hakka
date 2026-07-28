package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointSnapshotAndRestore(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewCheckpointStore(filepath.Join(dir, "ck"))
	if err != nil {
		t.Fatal(err)
	}

	// Create a test file.
	src := filepath.Join(dir, "test.txt")
	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Snapshot.
	id, err := cs.Snapshot(src)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty checkpoint ID")
	}
	if id[:3] != "ck_" {
		t.Fatalf("expected ID prefix ck_, got %q", id)
	}

	// Verify .bak file exists.
	_, base := cs.shardPath(id)
	bak := bakPath(base)
	bakData, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(bakData) != string(content) {
		t.Fatalf("bak content mismatch: got %q, want %q", string(bakData), string(content))
	}

	// Verify .meta file.
	metaData, err := os.ReadFile(metaPath(base))
	if err != nil {
		t.Fatal(err)
	}
	if string(metaData) == "" {
		t.Fatal("empty meta file")
	}

	// Modify original and restore.
	if err := os.WriteFile(src, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	restoredPath, err := cs.Restore(id)
	if err != nil {
		t.Fatal(err)
	}
	if restoredPath == "" {
		t.Fatal("expected non-empty restored path")
	}

	// Verify restored content.
	restoredData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredData) != string(content) {
		t.Fatalf("restored content mismatch: got %q, want %q", string(restoredData), string(content))
	}

	// Verify checkpoint files still exist (not deleted after restore).
	if _, err := os.Stat(bak); os.IsNotExist(err) {
		t.Fatal("expected .bak to persist after restore")
	}
	if _, err := os.Stat(metaPath(base)); os.IsNotExist(err) {
		t.Fatal("expected .meta to persist after restore")
	}

	// Restore again should succeed (checkpoint persists).
	restoredPath2, err := cs.Restore(id)
	if err != nil {
		t.Fatalf("expected second restore to succeed: %v", err)
	}
	if restoredPath2 == "" {
		t.Fatal("expected non-empty restored path on second restore")
	}
	// Verify content again.
	restoredData2, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredData2) != string(content) {
		t.Fatalf("second restore content mismatch: got %q, want %q", string(restoredData2), string(content))
	}
}

func TestCheckpointNilStore(t *testing.T) {
	var cs *CheckpointStore

	// Snapshot on nil should return ("", nil).
	id, err := cs.Snapshot("/tmp/test")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatal("expected empty ID from nil store")
	}

	// Restore on nil should error.
	_, err = cs.Restore("ck_test")
	if err == nil {
		t.Fatal("expected error from nil store restore")
	}
}

func TestCheckpointFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewCheckpointStore(filepath.Join(dir, "ck"))
	if err != nil {
		t.Fatal(err)
	}

	// File exactly at limit — should be checkpointed.
	srcAtLimit := filepath.Join(dir, "at_limit.bin")
	dataAtLimit := make([]byte, checkpointMaxFileSize)
	if err := os.WriteFile(srcAtLimit, dataAtLimit, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := cs.Snapshot(srcAtLimit)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("file at size limit should be checkpointed")
	}

	// Clean up.
	_, _ = cs.Restore(id)

	// File over limit — should be skipped.
	srcOver := filepath.Join(dir, "over_limit.bin")
	dataOver := make([]byte, checkpointMaxFileSize+1)
	if err := os.WriteFile(srcOver, dataOver, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err = cs.Snapshot(srcOver)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatal("file over size limit should be skipped")
	}
}

func TestCheckpointNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewCheckpointStore(filepath.Join(dir, "ck"))
	if err != nil {
		t.Fatal(err)
	}

	id, err := cs.Snapshot(filepath.Join(dir, "nonexistent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatal("nonexistent file should return empty ID")
	}
}

func TestCheckpointTotalLimitEviction(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewCheckpointStore(filepath.Join(dir, "ck"),
		WithMaxFileSize(1<<20),  // 1 MB per file
		WithTotalLimit(2<<20),   // 2 MB total — third file triggers eviction
	)
	if err != nil {
		t.Fatal(err)
	}

	// Create 3 files of 800 KB each (total 2.4 MB > 2 MB limit).
	// The first file should be evicted when the third is added.
	fileSize := 800 << 10 // 800 KB
	data := make([]byte, fileSize)

	var ids []string
	for i := 0; i < 3; i++ {
		fpath := filepath.Join(dir, "bigfile_"+string(rune('a'+i))+".bin")
		if err := os.WriteFile(fpath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := cs.Snapshot(fpath)
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			t.Fatalf("file %d should be checkpointed", i)
		}
		ids = append(ids, id)
	}

	// The first checkpoint should have been evicted.
	_, err = cs.Restore(ids[0])
	if err == nil {
		t.Fatal("expected first checkpoint to be evicted")
	}

	// The second and third should still exist.
	for _, id := range ids[1:] {
		_, err := cs.Restore(id)
		if err != nil {
			t.Fatalf("expected checkpoint %s to still exist: %v", id, err)
		}
	}
}

func TestCheckpointShardStructure(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewCheckpointStore(filepath.Join(dir, "ck"))
	if err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "shard_test.txt")
	if err := os.WriteFile(src, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := cs.Snapshot(src)
	if err != nil {
		t.Fatal(err)
	}

	_, base := cs.shardPath(id)

	// Verify shard directory structure.
	rel, _ := filepath.Rel(cs.rootDir, filepath.Dir(bakPath(base)))
	if len(rel) < 5 { // e.g. "a3/f2"
		t.Fatalf("expected sharded subdir like 'a3/f2', got %q", rel)
	}

	// Verify the .bak is in the right place.
	if _, err := os.Stat(bakPath(base)); err != nil {
		t.Fatal("expected .bak at", bakPath(base), err)
	}

	// Clean up.
	_, _ = cs.Restore(id)
}

// ---------------------------------------------------------------------------
// Shell heuristic tests
// ---------------------------------------------------------------------------

func TestShellHeuristicRedirect(t *testing.T) {
	dir := t.TempDir()

	// Create a file that exists.
	target := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("echo hello > output.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from redirect, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicSedInPlace(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(target, []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("sed -i 's/foo/bar/g' config.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from sed -i, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicSedInPlaceBackup(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "config.txt")
	if err := os.WriteFile(target, []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("sed -i.bak 's/foo/bar/g' config.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from sed -i.bak, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicCpMv(t *testing.T) {
	dir := t.TempDir()

	// For cp/mv, we checkpoint the destination if it already exists.
	dest := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("cp source.txt dest.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected dest from cp, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicTee(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("echo hello | tee log.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from tee, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicMultiplePatterns(t *testing.T) {
	dir := t.TempDir()

	// Create multiple target files.
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(f1, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("sed -i 's/x/y/' a.txt && echo hi > b.txt", dir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicNoExistingFiles(t *testing.T) {
	dir := t.TempDir()

	// File doesn't exist → should not be returned.
	files := GuessModifiedFiles("echo hello > nonexistent.txt", dir)
	if len(files) != 0 {
		t.Fatalf("expected 0 files for nonexistent target, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicRm(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(f1, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("rm a.txt b.txt", dir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files from rm, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicRmDashRf(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(f1, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("rm -rf data.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from rm -rf, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicRmAfterDashDash(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "-f.txt") // tricky filename starting with dash
	if err := os.WriteFile(f1, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := GuessModifiedFiles("rm -- -f.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from rm --, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicMvSource(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("src"), 0o644); err != nil {
		t.Fatal(err)
	}

	// mv src dst — both src (deleted) and dst (overwritten, if exists) should be checkpointed.
	files := GuessModifiedFiles("mv src.txt dst.txt", dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (src) from mv, got %d: %v", len(files), files)
	}
}

func TestShellHeuristicCpMultiToDir(t *testing.T) {
	dir := t.TempDir()

	// Create the destination directory with existing files.
	dstdir := filepath.Join(dir, "out")
	if err := os.MkdirAll(dstdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// These files will be overwritten by cp src1 src2 out/
	existing1 := filepath.Join(dstdir, "a.txt")
	existing2 := filepath.Join(dstdir, "b.txt")
	if err := os.WriteFile(existing1, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing2, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Source files don't need to exist — we're testing the path expansion.
	files := GuessModifiedFiles("cp a.txt b.txt out", dir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files (out/a.txt, out/b.txt) from cp multi-to-dir, got %d: %v", len(files), files)
	}
	if !containsPath(files, existing1) {
		t.Errorf("expected %s in result, got %v", existing1, files)
	}
	if !containsPath(files, existing2) {
		t.Errorf("expected %s in result, got %v", existing2, files)
	}
}

func TestShellHeuristicMvMultiToDir(t *testing.T) {
	dir := t.TempDir()

	src1 := filepath.Join(dir, "a.txt")
	src2 := filepath.Join(dir, "b.txt")
	dstdir := filepath.Join(dir, "out")
	if err := os.MkdirAll(dstdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Destination files that will be overwritten.
	existing1 := filepath.Join(dstdir, "a.txt")
	existing2 := filepath.Join(dstdir, "b.txt")
	if err := os.WriteFile(src1, []byte("src1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2, []byte("src2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing1, []byte("old1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing2, []byte("old2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// mv a.txt b.txt out/ →
	//   src1, src2 get deleted → extractMvSources returns [dir/a.txt, dir/b.txt]
	//   out/a.txt, out/b.txt get overwritten → extractCopyMoveDests returns [out/a.txt, out/b.txt]
	files := GuessModifiedFiles("mv a.txt b.txt out", dir)
	if len(files) != 4 {
		t.Fatalf("expected 4 files from mv multi-to-dir, got %d: %v", len(files), files)
	}
}

func containsPath(files []string, path string) bool {
	for _, f := range files {
		// Compare cleaned absolute paths.
		fa, _ := filepath.Abs(f)
		pa, _ := filepath.Abs(path)
		if fa == pa {
			return true
		}
	}
	return false
}
