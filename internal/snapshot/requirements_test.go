package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteRunMetaPreservesRequirementsSnapshot(t *testing.T) {
	dir := t.TempDir()
	for _, status := range []string{"running", "finalizing", "success"} {
		meta := RunMeta{Operator: "implement", Repo: "o/r", IssueNumber: 42, IssueTitle: "Original goal", IssueBody: "Original requirements", Status: status, StartedAt: time.Now().UTC()}
		if err := WriteRunMeta(dir, meta); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		var actual RunMeta
		if err := json.Unmarshal(b, &actual); err != nil || actual.IssueTitle != meta.IssueTitle || actual.IssueBody != meta.IssueBody {
			t.Fatalf("requirements lost: %+v %v", actual, err)
		}
	}
}
