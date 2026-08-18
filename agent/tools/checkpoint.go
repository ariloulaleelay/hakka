package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// checkpointMaxFileSize is the maximum file size (1 MB) eligible for checkpointing.
// Larger files are silently skipped.
const checkpointMaxFileSize = 1 << 20 // 1 MB

// checkpointTotalLimit is the maximum total size of all checkpoints (100 MB).
// When exceeded, the oldest checkpoints are evicted (LRU).
const checkpointTotalLimit = 100 << 20 // 100 MB

// checkpointMeta holds metadata about a single checkpoint.
type checkpointMeta struct {
	OriginalPath string    `json:"original_path"`
	Timestamp    time.Time `json:"timestamp"`
	Size         int64     `json:"size"`
}

// CheckpointStore provides file backup/restore capability for tool checkpoints.
// It is goroutine-safe. When the store is nil, all operations are no-ops.
//
// Storage layout:
//
//	<rootDir>/<XX>/<YY>/ck_<fullid>.bak     — file content copy
//	<rootDir>/<XX>/<YY>/ck_<fullid>.meta    — JSON metadata
//
// where <XX> and <YY> are the first 4 hex chars of the checkpoint ID
// to avoid thousands of files in a single directory.
type CheckpointStore struct {
	rootDir       string
	maxFileSize   int64 // per-file size limit (default: checkpointMaxFileSize)
	totalLimit    int64 // total storage limit (default: checkpointTotalLimit)
	totalBytes    atomic.Int64
	mu            sync.Mutex
	checkpointIDs []string                  // ordered by timestamp (oldest first) for LRU eviction
	checkpoints   map[string]checkpointMeta // id → meta
}

// CheckpointOption configures a CheckpointStore.
type CheckpointOption func(*CheckpointStore)

// WithMaxFileSize sets the per-file size limit for checkpointing.
func WithMaxFileSize(size int64) CheckpointOption {
	return func(cs *CheckpointStore) { cs.maxFileSize = size }
}

// WithTotalLimit sets the total storage limit before LRU eviction kicks in.
func WithTotalLimit(limit int64) CheckpointOption {
	return func(cs *CheckpointStore) { cs.totalLimit = limit }
}

// NewCheckpointStore creates a new CheckpointStore rooted at the given directory.
// The directory is created if it does not exist.
func NewCheckpointStore(rootDir string, opts ...CheckpointOption) (*CheckpointStore, error) {
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("checkpoint: create root dir: %w", err)
	}
	cs := &CheckpointStore{
		rootDir:       rootDir,
		maxFileSize:   checkpointMaxFileSize,
		totalLimit:    checkpointTotalLimit,
		checkpointIDs: make([]string, 0),
		checkpoints:   make(map[string]checkpointMeta),
	}
	for _, opt := range opts {
		opt(cs)
	}
	return cs, nil
}

// DefaultCheckpointDir returns the default checkpoint root directory.
func DefaultCheckpointDir() string {
	return filepath.Join(os.TempDir(), "hakka-checkpoints")
}

// generateID returns a random checkpoint ID.
func generateID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ck_" + hex.EncodeToString(b[:]), nil
}

// shardPath returns the sharded directory and full base path for a checkpoint ID.
// Example: id="ck_a3f2b901" → subdir="a3/f2/", base="<root>/a3/f2/ck_a3f2b901"
func (cs *CheckpointStore) shardPath(id string) (string, string) {
	// ID format: "ck_" + 8 hex chars
	hexPart := id[3:] // strip "ck_"
	subdir := filepath.Join(hexPart[0:2], hexPart[2:4])
	base := filepath.Join(cs.rootDir, subdir, id)
	return filepath.Join(cs.rootDir, subdir), base
}

// bakPath returns the .bak file path.
func bakPath(base string) string {
	return base + ".bak"
}

// metaPath returns the .meta file path.
func metaPath(base string) string {
	return base + ".meta"
}

// Snapshot creates a checkpoint of the given file. Returns the checkpoint ID
// or ("", nil) if the file was skipped (doesn't exist, exceeds size limit, etc.).
// Returns ("", error) only on real I/O errors.
func (cs *CheckpointStore) Snapshot(filePath string) (string, error) {
	if cs == nil {
		return "", nil
	}

	info, err := os.Stat(filePath)
	if err != nil {
		// File doesn't exist — nothing to checkpoint (not an error).
		return "", nil
	}

	if info.Size() > cs.maxFileSize {
		return "", nil // file too large
	}

	id, err := generateID()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	subdir, base := cs.shardPath(id)
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		return "", err
	}

	if err := os.WriteFile(bakPath(base), data, 0o600); err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	meta := checkpointMeta{
		OriginalPath: absPath,
		Timestamp:    time.Now(),
		Size:         info.Size(),
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		// Clean up the .bak file we just wrote.
		_ = os.Remove(bakPath(base))
		return "", err
	}
	if err := os.WriteFile(metaPath(base), metaJSON, 0o600); err != nil {
		_ = os.Remove(bakPath(base))
		return "", err
	}

	cs.mu.Lock()
	cs.checkpointIDs = append(cs.checkpointIDs, id)
	cs.checkpoints[id] = meta
	cs.mu.Unlock()

	cs.totalBytes.Add(info.Size())

	// Evict old checkpoints if we exceed the total limit.
	cs.evictIfNeeded()

	return id, nil
}

// Restore copies the checkpointed content back to the original file path.
// Returns the original file path on success. The checkpoint is consumed
// (deleted) after successful restore.
func (cs *CheckpointStore) Restore(id string) (string, error) {
	if cs == nil {
		return "", fmt.Errorf("checkpoints are not available")
	}

	cs.mu.Lock()
	meta, ok := cs.checkpoints[id]
	if !ok {
		cs.mu.Unlock()
		return "", fmt.Errorf("checkpoint %q not found", id)
	}
	cs.mu.Unlock()

	_, base := cs.shardPath(id)
	bak := bakPath(base)

	data, err := os.ReadFile(bak)
	if err != nil {
		return "", fmt.Errorf("checkpoint %q: read backup: %w", id, err)
	}

	if err := os.MkdirAll(filepath.Dir(meta.OriginalPath), 0o755); err != nil {
		return "", fmt.Errorf("checkpoint %q: create parent dirs: %w", id, err)
	}

	if err := os.WriteFile(meta.OriginalPath, data, 0o644); err != nil {
		return "", fmt.Errorf("checkpoint %q: restore file: %w", id, err)
	}

	// Note: checkpoint files are NOT deleted after restore.
	// They persist until LRU eviction (total limit) reclaims the space.
	// This allows the user to rollback to the same checkpoint multiple times,
	// or navigate through history with multiple checkpoints.

	return meta.OriginalPath, nil
}

// evictIfNeeded removes the oldest checkpoints until total size is under limit.
// Must be called with cs.mu NOT held (it locks internally).
func (cs *CheckpointStore) evictIfNeeded() {
	for cs.totalBytes.Load() > cs.totalLimit {
		cs.mu.Lock()
		if len(cs.checkpointIDs) == 0 {
			cs.mu.Unlock()
			return
		}
		// Find the oldest checkpoint.
		oldest := cs.checkpointIDs[0]
		oldestMeta, ok := cs.checkpoints[oldest]
		cs.mu.Unlock()

		if !ok {
			// Inconsistent state — remove from slice and retry.
			cs.mu.Lock()
			if len(cs.checkpointIDs) > 0 {
				cs.checkpointIDs = cs.checkpointIDs[1:]
			}
			cs.mu.Unlock()
			continue
		}

		// Evict oldest.
		_, base := cs.shardPath(oldest)
		_ = os.Remove(bakPath(base))
		_ = os.Remove(metaPath(base))
		_ = os.Remove(filepath.Dir(bakPath(base)))
		_ = os.Remove(filepath.Dir(filepath.Dir(bakPath(base))))

		cs.mu.Lock()
		if len(cs.checkpointIDs) > 0 && cs.checkpointIDs[0] == oldest {
			cs.checkpointIDs = cs.checkpointIDs[1:]
			delete(cs.checkpoints, oldest)
		}
		cs.mu.Unlock()

		cs.totalBytes.Add(-oldestMeta.Size)
	}
}

// ---------------------------------------------------------------------------
// Shell heuristic — guess which files a shell command might modify.
// ---------------------------------------------------------------------------

// shellFilePatterns defines regex-like patterns to extract potential file
// paths from shell commands. Each pattern has a function that extracts
// paths from the command string.
//
// The heuristics are intentionally simple and conservative:
//   - Only detect files that already exist on disk (checked by the caller).
//   - Focus on the most common modification patterns.
var shellFilePatterns = []func(cmd, cwd string) []string{
	// > file, >> file, 2> file, &> file
	func(cmd, cwd string) []string {
		return extractRedirectTargets(cmd, cwd)
	},
	// sed -i ... file
	func(cmd, cwd string) []string {
		return extractSedInPlace(cmd, cwd)
	},
	// cp src dst, mv src dst (destination)
	func(cmd, cwd string) []string {
		return extractCopyMoveDests(cmd, cwd)
	},
	// mv src dst — also checkpoint source (it gets deleted)
	func(cmd, cwd string) []string {
		return extractMvSources(cmd, cwd)
	},
	// rm file [file...]
	func(cmd, cwd string) []string {
		return extractRmTargets(cmd, cwd)
	},
	// tee file
	func(cmd, cwd string) []string {
		return extractTeeTargets(cmd, cwd)
	},
}

// GuessModifiedFiles applies shell heuristics to identify files that
// might be modified by the given shell command. Only files that already
// exist on disk are returned.
func GuessModifiedFiles(cmd, cwd string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, fn := range shellFilePatterns {
		for _, f := range fn(cmd, cwd) {
			if f == "" {
				continue
			}
			// Resolve relative paths.
			if !filepath.IsAbs(f) {
				if cwd != "" {
					f = filepath.Join(cwd, f)
				}
			}
			abs, err := filepath.Abs(f)
			if err != nil {
				continue
			}
			if seen[abs] {
				continue
			}
			info, err := os.Stat(abs)
			if err != nil || info.IsDir() {
				continue
			}
			seen[abs] = true
			result = append(result, abs)
		}
	}

	sort.Strings(result)
	return result
}

// extractRedirectTargets extracts file paths from shell redirections.
// Matches: >file, >>file, 2>file, 2>>file, &>file, 1>file, | tee file
func extractRedirectTargets(cmd, cwd string) []string {
	var result []string
	// Simple token-based extraction.
	tokens := shellSplit(cmd)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		// Check for redirection operators.
		// Patterns: N>, N>>, >, >>, &>, &>>
		if isRedirectOp(t) && i+1 < len(tokens) {
			result = append(result, tokens[i+1])
		}
		// Handle 2>file (no space) and >file (no space).
		if stripped := stripRedirectPrefix(t); stripped != "" && stripped != t {
			result = append(result, stripped)
		}
	}
	return result
}

// extractSedInPlace extracts file paths from sed -i commands.
func extractSedInPlace(cmd, cwd string) []string {
	var result []string
	tokens := shellSplit(cmd)
	for i, t := range tokens {
		if t == "sed" && i+3 < len(tokens) {
			// sed -i expr file  (4 tokens: sed, -i, expr, file)
			// sed -i.bak expr file
			if tokens[i+1] == "-i" {
				result = append(result, tokens[i+3])
			} else if strings.HasPrefix(tokens[i+1], "-i") {
				result = append(result, tokens[i+3])
			}
		}
	}
	return result
}

// extractCopyMoveDests extracts destination file paths from cp and mv commands.
// For cp/mv src dst → returns [dst].
// For cp/mv src1 src2 ... dstdir/ → returns [dstdir/src1_basename, dstdir/src2_basename, ...].
// For mv src dst, the source is also extracted by extractMvSources.
func extractCopyMoveDests(cmd, cwd string) []string {
	tokens := shellSplit(cmd)
	if len(tokens) < 3 {
		return nil
	}
	if tokens[0] != "cp" && tokens[0] != "mv" {
		return nil
	}

	// Separate flags from file arguments.
	var files []string
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if strings.HasPrefix(t, "-") {
			continue
		}
		files = append(files, t)
	}

	if len(files) < 2 {
		return nil // need at least src + dst
	}

	// Last file argument is the destination.
	dst := files[len(files)-1]
	srcs := files[:len(files)-1]

	if len(srcs) == 1 {
		// Single source: the destination is a file path (might overwrite).
		return []string{dst}
	}

	// Multiple sources: the destination must be a directory.
	// Expand: return dstdir/<basename> for each source.
	resolvedDst := dst
	if !filepath.IsAbs(dst) && cwd != "" {
		resolvedDst = filepath.Join(cwd, dst)
	}
	dstInfo, err := os.Stat(resolvedDst)
	if err != nil || !dstInfo.IsDir() {
		return nil // not a directory, can't expand
	}

	var result []string
	for _, src := range srcs {
		result = append(result, filepath.Join(dst, filepath.Base(src)))
	}
	return result
}

// extractMvSources extracts source file paths from mv commands.
// The source is destroyed by mv, so it should be checkpointed too.
func extractMvSources(cmd, cwd string) []string {
	var result []string
	tokens := shellSplit(cmd)
	if len(tokens) < 3 {
		return result
	}
	if tokens[0] != "mv" {
		return result
	}
	// Everything between mv and the last argument (except flags) is a source.
	// mv [-f] [-i] [-n] [-v] src dst
	// mv [-f] [-i] [-n] [-v] src1 src2 ... dst_dir
	lastIdx := len(tokens) - 1
	for i := 1; i < lastIdx; i++ {
		t := tokens[i]
		if strings.HasPrefix(t, "-") {
			continue
		}
		result = append(result, t)
	}
	return result
}

// extractRmTargets extracts file paths from rm commands.
func extractRmTargets(cmd, cwd string) []string {
	var result []string
	tokens := shellSplit(cmd)
	if len(tokens) < 2 {
		return result
	}
	if tokens[0] != "rm" {
		return result
	}
	// All non-flag arguments after rm are file paths.
	for i := 1; i < len(tokens); i++ {
		t := tokens[i]
		if strings.HasPrefix(t, "-") {
			// Skip flags like -rf, -f, -r, --force etc.
			// But stop scanning if we see "--" (end of flags marker).
			if t == "--" {
				// Everything after -- is a file path.
				for j := i + 1; j < len(tokens); j++ {
					result = append(result, tokens[j])
				}
				return result
			}
			continue
		}
		result = append(result, t)
	}
	return result
}

// extractTeeTargets extracts file paths from tee commands.
func extractTeeTargets(cmd, cwd string) []string {
	var result []string
	tokens := shellSplit(cmd)
	for i, t := range tokens {
		if t == "tee" {
			// tee [-a] file [file...]
			for j := i + 1; j < len(tokens); j++ {
				tok := tokens[j]
				if tok == "-a" {
					continue
				}
				if strings.HasPrefix(tok, "-") {
					continue
				}
				result = append(result, tok)
			}
			break
		}
	}
	return result
}

// isRedirectOp returns true if the token is a shell redirection operator.
func isRedirectOp(t string) bool {
	return t == ">" || t == ">>" || t == "2>" || t == "2>>" ||
		t == "1>" || t == "1>>" || t == "&>" || t == "&>>"
}

// stripRedirectPrefix handles cases like ">file" or "2>file" (no space).
func stripRedirectPrefix(t string) string {
	// Try 2>>file, 2>file, 1>>file, 1>file, &>file, >>file, >file
	for _, prefix := range []string{"2>>", "2>", "1>>", "1>", "&>>", "&>", ">>", ">"} {
		if strings.HasPrefix(t, prefix) && len(t) > len(prefix) {
			return t[len(prefix):]
		}
	}
	return t
}

// shellSplit performs a naive shell tokenization (splits on whitespace,
// respecting single and double quotes).
func shellSplit(cmd string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				current.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else {
				current.WriteByte(ch)
			}
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == ' ' || ch == '\t':
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
