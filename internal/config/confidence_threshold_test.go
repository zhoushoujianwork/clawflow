package config_test

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// intp is a small helper for building *int literals in table tests.
func intp(v int) *int { return &v }

// TestEffectiveConfidenceThreshold covers issue #336: config must be the
// single source of truth for the evaluate-*/salvage pass/fail bar, and must
// distinguish "never configured" from "explicitly set to 0".
func TestEffectiveConfidenceThreshold(t *testing.T) {
	cases := []struct {
		name      string
		threshold *int
		want      float64
	}{
		{"absent defaults to 7", nil, 7},
		{"explicit zero means every valid score passes", intp(0), 0},
		{"explicit 6", intp(6), 6},
		{"explicit 8", intp(8), 8},
		{"boundary 10 is valid", intp(10), 10},
		{"boundary 0 is valid (same as explicit zero)", intp(0), 0},
		{"negative falls back to default", intp(-1), 7},
		{"over 10 falls back to default", intp(11), 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := config.Settings{ConfidenceThreshold: tc.threshold}
			got := s.EffectiveConfidenceThreshold()
			if got != tc.want {
				t.Errorf("EffectiveConfidenceThreshold() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEffectiveConfidenceThreshold_NilSettings guards the nil-receiver case
// so a caller that forgot to load config doesn't panic and instead gets the
// safe default.
func TestEffectiveConfidenceThreshold_NilSettings(t *testing.T) {
	var s *config.Settings
	if got := s.EffectiveConfidenceThreshold(); got != 7 {
		t.Errorf("nil Settings: EffectiveConfidenceThreshold() = %v, want 7", got)
	}
}
