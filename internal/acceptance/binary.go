package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

// BinaryID changes even for rebuilt development binaries with the same version.
var BinaryID = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil))
})

// ConsecutiveInfrastructure counts only the trailing infrastructure failures
// from this binary. A verdict or a binary change starts a fresh retry budget.
func ConsecutiveInfrastructure(records []Record) InfrastructureAttempts {
	var out InfrastructureAttempts
	if len(records) == 0 {
		return out
	}
	out.LatestIsInfra = IsInfrastructure(records[len(records)-1])
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if !IsInfrastructure(r) || r.BinaryID != BinaryID() || r.Contract.Requirements != records[len(records)-1].Contract.Requirements {
			break
		}
		out.Count++
		if r.CompletedAt.After(out.LastAt) {
			out.LastAt = r.CompletedAt
		}
	}
	return out
}
