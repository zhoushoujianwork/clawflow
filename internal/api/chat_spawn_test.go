package api

import (
	"testing"
)

// TestShellEscape verifies that shellEscape produces correct POSIX
// single-quoted strings for a range of inputs, including the '"'"'
// dance for embedded single quotes.
func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "''",
		},
		{
			name:  "plain word",
			input: "hello",
			want:  "'hello'",
		},
		{
			name:  "string with spaces",
			input: "hello world",
			want:  "'hello world'",
		},
		{
			name:  "single quote in string",
			input: "it's",
			want:  "'it'\"'\"'s'",
		},
		{
			name:  "repo name with single quote",
			input: "owner/it's-a-repo",
			want:  "'owner/it'\"'\"'s-a-repo'",
		},
		{
			name:  "double quote in string",
			input: `a"b`,
			want:  `'a"b'`,
		},
		{
			name:  "backtick in string",
			input: "a`b",
			want:  "'a`b'",
		},
		{
			name:  "dollar sign",
			input: "$HOME",
			want:  "'$HOME'",
		},
		{
			name:  "newline in string",
			input: "line\nbreak",
			want:  "'line\nbreak'",
		},
		{
			name:  "multiple single quotes",
			input: "it's a 'test'",
			want:  "'it'\"'\"'s a '\"'\"'test'\"'\"''",
		},
		{
			name:  "path with spaces",
			input: "/usr/local/bin/my app",
			want:  "'/usr/local/bin/my app'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscape(tc.input)
			if got != tc.want {
				t.Errorf("shellEscape(%q)\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestAppleScriptQuote verifies that appleScriptQuote produces correct
// AppleScript double-quoted string literals, escaping backslashes and
// double quotes.
func TestAppleScriptQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  `""`,
		},
		{
			name:  "plain word",
			input: "hello",
			want:  `"hello"`,
		},
		{
			name:  "string with spaces",
			input: "hello world",
			want:  `"hello world"`,
		},
		{
			name:  "embedded double quote",
			input: `say "hi"`,
			want:  `"say \"hi\""`,
		},
		{
			name:  "backslash",
			input: `path\to`,
			want:  `"path\\to"`,
		},
		{
			name:  "backslash and double quote",
			input: `a\"b`,
			want:  `"a\\\"b"`,
		},
		{
			name:  "newline — documented current behavior (literal newline, not escaped)",
			input: "line\nbreak",
			want:  "\"line\nbreak\"",
		},
		{
			name:  "single quote — no escaping needed in AppleScript double-quoted strings",
			input: "it's",
			want:  `"it's"`,
		},
		{
			name:  "app name with spaces",
			input: "Visual Studio Code",
			want:  `"Visual Studio Code"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := appleScriptQuote(tc.input)
			if got != tc.want {
				t.Errorf("appleScriptQuote(%q)\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestDetectVSCodeAppName verifies the app-name detection heuristic
// that maps a resolved binary path to the correct application name.
func TestDetectVSCodeAppName(t *testing.T) {
	tests := []struct {
		name    string
		codeBin string
		want    string
	}{
		{
			name:    "standard VS Code path",
			codeBin: "/usr/local/bin/code",
			want:    "Visual Studio Code",
		},
		{
			name:    "Cursor path",
			codeBin: "/Applications/Cursor.app/Contents/MacOS/cursor",
			want:    "Cursor",
		},
		{
			name:    "Qoder path",
			codeBin: "/Applications/Qoder.app/Contents/MacOS/qoder",
			want:    "Qoder",
		},
		{
			name:    "VSCodium path",
			codeBin: "/Applications/VSCodium.app/Contents/MacOS/vscodium",
			want:    "VSCodium",
		},
		{
			name:    "VS Code Insiders path",
			codeBin: "/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/code",
			want:    "Visual Studio Code - Insiders",
		},
		{
			name:    "unknown binary falls back to VS Code",
			codeBin: "/usr/local/bin/myeditor",
			want:    "Visual Studio Code",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectVSCodeAppName(tc.codeBin)
			if got != tc.want {
				t.Errorf("detectVSCodeAppName(%q)\n  got  %q\n  want %q", tc.codeBin, got, tc.want)
			}
		})
	}
}
