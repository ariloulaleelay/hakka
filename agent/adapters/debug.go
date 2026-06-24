package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// reqCounter provides globally-unique sequence numbers for debug dumps
// so that concurrent and sequential requests never collide on filenames.
var reqCounter atomic.Int64

// dumpDebugJSON writes v as indented JSON to a timestamped file under dir.
// The filename includes prefix (e.g. "openai", "anthropic"), a nanosecond
// timestamp, a monotonically-increasing counter, and a human-readable label
// (e.g. "req", "resp", "stream_resp").
//
// When dir is empty the call is a no-op — this is the common case in
// production and avoids any allocation or I/O overhead.
func dumpDebugJSON(dir, prefix, label string, v any) {
	if dir == "" {
		return
	}
	n := reqCounter.Add(1)
	ts := time.Now().UnixNano()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%s_%d_%d_%s.json", prefix, ts, n, label))
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0644)
}
