package acceptance

import "testing"

func TestUnfenceReportStrictDecode(t *testing.T) {
	const obj = `{"status":"passed","summary":"ok","evidence":["e"]}`
	type report struct {
		Status   Verdict  `json:"status"`
		Summary  string   `json:"summary"`
		Evidence []string `json:"evidence"`
	}
	for name, tc := range map[string]struct {
		text string
		ok   bool
	}{
		"raw":                {obj, true},
		"json fence":         {"```json\n" + obj + "\n```", true},
		"bare fence":         {"\n```\n" + obj + "\n```\n", true},
		"prose around":       {"Here:\n```json\n" + obj + "\n```", false},
		"trailing prose":     {"```json\n" + obj + "\n```\nDone.", false},
		"two blocks":         {"```json\n" + obj + "\n```\n```json\n" + obj + "\n```", false},
		"other language":     {"```yaml\n" + obj + "\n```", false},
		"unterminated":       {"```json\n" + obj, false},
		"two objects fenced": {"```json\n" + obj + obj + "\n```", false},
		"case variant key":   {"```json\n" + `{"Status":"passed","summary":"ok","evidence":["e"]}` + "\n```", false},
	} {
		t.Run(name, func(t *testing.T) {
			var r report
			err := decodeStrict([]byte(unfenceReport(tc.text)), &r)
			if (err == nil) != tc.ok {
				t.Fatalf("decode err=%v, want ok=%v", err, tc.ok)
			}
			if tc.ok && r.Status != Passed {
				t.Fatalf("status=%q", r.Status)
			}
		})
	}
}
