package coordinator

import (
	"encoding/json"
	"testing"
)

func TestLegacyInterruptionAdoption(t *testing.T) {
	for _, mode := range []string{"interrupted", "success", "other-failure", "effects", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _ := setup(t)
			register(t, s, "w", 1)
			task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":3600}`)})
			if err != nil {
				t.Fatal(err)
			}
			leased := claim(t, s, "w")
			report := map[string]any{"status": "failed", "error": "Offload execution: canceled"}
			if mode == "success" {
				report["status"] = "success"
			}
			if mode == "other-failure" {
				report["error"] = "real code failure"
			}
			if mode == "effects" {
				report["effects"] = []map[string]any{{"kind": "issue-comment", "repo": "o/r", "number": 1, "body": "retained effect"}}
			}
			raw, _ := json.Marshal(report)
			if err := s.AcceptResult(ctx, task.ID, "w", leased.FencingToken, raw); err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				if err := s.Cancel(ctx, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := s.AdoptLegacyInterruption(ctx, task.ID, "w", leased.FencingToken, raw)
			if mode != "interrupted" {
				if err == nil {
					t.Fatal("unsafe legacy report adopted", mode)
				}
				return
			}
			if err != nil || !DurableTask(recovered) || recovered.Result != nil || recovered.FencingToken != leased.FencingToken+1 || recovered.Attempt != 1 {
				t.Fatal(recovered, err)
			}
			replay, err := s.AdoptLegacyInterruption(ctx, task.ID, "w", leased.FencingToken, raw)
			if err != nil || replay.FencingToken != recovered.FencingToken {
				t.Fatal("offline import replay changed authority", replay, err)
			}
		})
	}
}
