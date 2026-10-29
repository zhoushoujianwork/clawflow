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
		{"Edit `internal/acceptance/runner.go`, `contract.go` and `store.go`.", []string{"internal/acceptance/runner.go", "contract.go", "internal/acceptance/contract.go", "store.go", "internal/acceptance/store.go"}},
		{"No explicit files", nil},
		{"https://example.com/foo.go /tmp/private.go ../secret.go", nil},
		{"Update ./web/src/app.tsx", []string{"web/src/app.tsx"}},
		{"Update go.mod", []string{"go.mod"}},
		{"Update Dockerfile", []string{"Dockerfile"}},
		{"Update api/service.proto", []string{"api/service.proto"}},
		{"Update `Buildfile`", []string{"Buildfile"}},
		{"Update scripts/build", []string{"scripts/build"}},
		{"Update `.editorconfig`.", []string{".editorconfig"}},
		{"No explicit files.", nil},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := EstimateFiles(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%q: %v, want %v", tc.text, got, tc.want)
			}
		})
	}
	if !FilesOverlap([]string{"internal/store/migrations/005.sql"}, []string{"internal/store/migrations.go"}) {
		t.Fatal("migration namespace must overlap")
	}
	if FilesOverlap([]string{"internal/a.go"}, []string{"internal/b.go"}) {
		t.Fatal("independent files overlap")
	}
}

func TestEstimateMixedRootAndNestedFiles(t *testing.T) {
	for _, root := range []string{"go.mod", "go.sum", "Dockerfile", "Makefile", "README.md", ".editorconfig", "Buildfile"} {
		t.Run(root, func(t *testing.T) {
			files := EstimateFiles("Edit `internal/a.go` and `" + root + "`.")
			if !FilesOverlap(files, []string{root}) {
				t.Fatalf("explicit root file %q lost in estimate: %v", root, files)
			}
			if !FilesOverlap(files, []string{"internal/a.go"}) {
				t.Fatalf("nested file lost in estimate: %v", files)
			}
		})
	}
}
