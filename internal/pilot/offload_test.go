package pilot

import (
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
	"testing"
)

func TestOffloadHelper(t *testing.T) { offloadtest.Helper(t) }
func stubOffloadFailure(t *testing.T, kind, text string) {
	offloadtest.Setup(t)
	t.Setenv("OFFLOAD_TEST_ERROR", kind)
	t.Setenv("OFFLOAD_TEST_TEXT", text)
}
