package chat

import (
	"sort"
	"testing"
)

// init() in kinds.go registers "context" — these tests rely on that
// baseline state.

func TestGetKind_Known(t *testing.T) {
	k, ok := GetKind("context")
	if !ok {
		t.Fatal(`GetKind("context"): not registered`)
	}
	if k.Name != "context" {
		t.Errorf(`GetKind("context").Name = %q`, k.Name)
	}
	if k.Builder == nil {
		t.Error(`GetKind("context").Builder is nil`)
	}
	if k.DraftTag == "" {
		t.Error(`GetKind("context").DraftTag is empty`)
	}
}

func TestGetKind_Unknown(t *testing.T) {
	if _, ok := GetKind("does-not-exist"); ok {
		t.Error("GetKind returned ok for unknown kind")
	}
}

func TestListKinds_Sorted(t *testing.T) {
	kinds := ListKinds()
	if len(kinds) < 1 {
		t.Fatalf("ListKinds: expected >= 1 kind, got %d", len(kinds))
	}
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("ListKinds is not sorted: %v", names)
	}
}

func TestRegisterKind_DuplicatePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterKind on duplicate name should panic")
		}
	}()
	RegisterKind(ChatKind{
		Name:    "context", // already registered in init()
		Builder: func(string, []ProjectChatRepo, string) string { return "" },
	})
}

func TestContextKindAdapter_PassesEmptyTesting(t *testing.T) {
	// The "context" kind is an adapter wrapping BuildProjectChatContext
	// with an empty testingMD. Smoke-check it produces a prompt without
	// blowing up; project-chat's own tests cover the inner builder.
	k, ok := GetKind("context")
	if !ok {
		t.Fatal("context kind not registered")
	}
	out := k.Builder("p", []ProjectChatRepo{{Name: "owner/r", LocalPath: "/tmp/r"}}, "# ctx")
	if !containsStr(out, "p") || !containsStr(out, "# ctx") {
		t.Error("context adapter output missing expected fields")
	}
}
