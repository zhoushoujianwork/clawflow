package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAGHUsageRemainsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	// Native-looking raw evidence must not be interpreted as known AGH usage.
	data := `{"version":1,"request_id":"r","seq":1,"type":"observation","observation":{"type":"text_delta","delta":"done","raw":{"type":"assistant","message":{"usage":{"input_tokens":123}}}}}` + "\n" + `{"version":1,"request_id":"r","seq":2,"type":"result","result":{"status":"completed","text":"done","session_key":"s","usage":null}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractUsage(path)
	if err != nil || got != nil {
		t.Fatalf("usage=%+v err=%v", got, err)
	}
}
