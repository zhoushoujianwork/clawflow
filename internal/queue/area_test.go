package queue

import (
	"reflect"
	"testing"
)

func TestEstimateFiles(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"Edit `internal/acceptance/runner.go`, `contract.go` and `store.go`.", []string{"internal/acceptance/runner.go", "internal/acceptance/contract.go", "internal/acceptance/store.go"}},
		{"No explicit files", nil},
		{"https://example.com/foo.go /tmp/private.go ../secret.go", nil},
		{"Update ./web/src/app.tsx", []string{"web/src/app.tsx"}},
	} {
		if got := EstimateFiles(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %v, want %v", tc.text, got, tc.want)
		}
	}
	if !FilesOverlap([]string{"internal/store/migrations/005.sql"}, []string{"internal/store/migrations.go"}) {
		t.Fatal("migration namespace must overlap")
	}
	if FilesOverlap([]string{"internal/a.go"}, []string{"internal/b.go"}) {
		t.Fatal("independent files overlap")
	}
}
