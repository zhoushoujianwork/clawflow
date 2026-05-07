package chat

import (
	"sort"
	"testing"
)

// init() in kinds.go registers "goals" and "context" — these tests
// rely on that baseline state.

func TestGetKind_Known(t *testing.T) {
	for _, name := range []string{"goals", "context"} {
		k, ok := GetKind(name)
		if !ok {
			t.Errorf("GetKind(%q): not registered", name)
			continue
		}
		if k.Name != name {
			t.Errorf("GetKind(%q).Name = %q", name, k.Name)
		}
		if k.Builder == nil {
			t.Errorf("GetKind(%q).Builder is nil", name)
		}
		if k.DraftTag == "" {
			t.Errorf("GetKind(%q).DraftTag is empty", name)
		}
	}
}

func TestGetKind_Unknown(t *testing.T) {
	if _, ok := GetKind("does-not-exist"); ok {
		t.Error("GetKind returned ok for unknown kind")
	}
}

func TestListKinds_Sorted(t *testing.T) {
	kinds := ListKinds()
	if len(kinds) < 2 {
		t.Fatalf("ListKinds: expected >= 2 kinds, got %d", len(kinds))
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
		Name:    "goals", // already registered in init()
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
