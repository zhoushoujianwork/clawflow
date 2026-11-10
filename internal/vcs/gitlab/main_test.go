package gitlab

import (
	"os"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/retry"
)

// Retry fixtures use millisecond backoff so the suite does not really wait
// 1/2/4 seconds per transient response; tests that check the production
// schedule set their own config.
func TestMain(m *testing.M) {
	retry.DefaultConfig = retry.Config{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	os.Exit(m.Run())
}
