package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestRunnerRejectsNonexactReportKeys(t *testing.T) {
	for name, report := range map[string]string{
		"lower first":   `{"status":"failed","Status":"passed","summary":"ok","evidence":["ok"]}`,
		"upper first":   `{"Status":"passed","status":"failed","summary":"ok","evidence":["ok"]}`,
		"lone Status":   `{"Status":"passed","summary":"ok","evidence":["ok"]}`,
		"lone SUMMARY":  `{"status":"passed","SUMMARY":"ok","evidence":["ok"]}`,
		"lone Evidence": `{"status":"passed","summary":"ok","Evidence":["ok"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			base := r.Execute
			r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
				result, err := base(ctx, prompt, opts)
				if !opts.ToolsDisabled {
					result.Text = report
				}
				return result, err
			}
			record, err := r.Run(context.Background(), contract(), scratch, workspace)
			if err == nil || record.Verdict != Blocked || Evaluate(contract(), record) == nil {
				t.Fatal("accepted nonexact report keys")
			}
			stored, err := r.Store.LoadRecord(contract(), record.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			if Evaluate(contract(), stored) == nil {
				t.Fatal("stored nonexact report satisfied gate")
			}
		})
	}
}

func TestStoreRejectsNonexactSchemaKeys(t *testing.T) {
	for name, change := range map[string]func(string) string{
		"Contract":       func(s string) string { return strings.Replace(s, `"contract":`, `"Contract":`, 1) },
		"nested HeadSHA": func(s string) string { return strings.Replace(s, `"head_sha":`, `"HEAD_SHA":`, 1) },
		"contract duplicate lower first": func(s string) string {
			return strings.Replace(s, `"head_sha":"head"`, `"head_sha":"wrong","HEAD_SHA":"head"`, 1)
		},
		"contract duplicate upper first": func(s string) string {
			return strings.Replace(s, `"head_sha":"head"`, `"HEAD_SHA":"head","head_sha":"wrong"`, 1)
		},
		"embedded Identity": func(s string) string {
			return strings.Replace(s, `"session_id":"plan-session"`, `"Session_ID":"plan-session"`, 1)
		},
		"unknown nested field": func(s string) string { return strings.Replace(s, `"task_id":`, `"unexpected":true,"task_id":`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			r := passing()
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			modified := json.RawMessage(change(string(data)))
			if string(modified) == string(data) {
				t.Fatal("fixture did not change")
			}
			ref, _ := recordName(r.Contract, r.AttemptID)
			// write recomputes a valid envelope digest; rejection must come from
			// recursive schema validation rather than corruption detection.
			if err := s.write(ref, modified); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadRecord(r.Contract, r.AttemptID); err == nil {
				t.Fatal("loaded nonexact schema")
			}
			if _, err := s.LatestRecord(r.Contract); err == nil {
				t.Fatal("latest accepted nonexact schema")
			}
		})
	}
}

func TestStoreRejectsNonexactEnvelopeKeys(t *testing.T) {
	for _, key := range []string{"Version", "Digest", "Data"} {
		t.Run(key, func(t *testing.T) {
			s := openTestStore(t)
			r := passing()
			if err := s.SaveRecord(r); err != nil {
				t.Fatal(err)
			}
			ref, _ := recordName(r.Contract, r.AttemptID)
			path := filepath.Join(s.path, ref, "data.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			modified := strings.Replace(string(data), `"`+strings.ToLower(key)+`":`, `"`+key+`":`, 1)
			if err := os.WriteFile(path, []byte(modified), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadRecord(r.Contract, r.AttemptID); err == nil {
				t.Fatal("loaded nonexact envelope")
			}
		})
	}
}

func TestExactSchemaPreservesEmbeddedAndOptionalFields(t *testing.T) {
	r := passing()
	r.Contract.IntegrationSHA = ""
	r.ContractDigest, _ = r.Contract.Digest()
	r.Plan.ContractDigest = r.ContractDigest
	r.Before.IntegrationSHA = ""
	r.After.IntegrationSHA = ""
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := decodeStrict(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(r.Contract, decoded); err != nil {
		t.Fatal(err)
	}
}
