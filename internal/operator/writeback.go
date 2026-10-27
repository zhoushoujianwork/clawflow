package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// writeBack stores only the completed execution's delivery intent, never credentials.
type writeBack struct {
	Operator Operator
	Subject  Subject
	Repo     string
	RunID    string
	Body     string
	Outcome  string
}

func saveWriteBack(op *Operator, sub *Subject, opts RunOptions, body, outcome string) error {
	intent := writeBack{
		Operator: Operator{Name: op.Name, Outcomes: op.Outcomes, Trigger: Trigger{LabelsConsumed: op.Trigger.LabelsConsumed}},
		Subject:  Subject{Number: sub.Number}, Repo: opts.Repo, RunID: opts.RunID, Body: body, Outcome: outcome,
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	path := filepath.Join(opts.CommentSaveDir, "writeback.json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// ReplayWriteBack retries the saved, idempotent delivery without invoking an agent.
func ReplayWriteBack(v VCS, dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "writeback.json"))
	if err != nil {
		return err
	}
	var pending writeBack
	if err := json.Unmarshal(data, &pending); err != nil {
		return err
	}
	opts := RunOptions{Repo: pending.Repo, RunID: pending.RunID, CommentSaveDir: dir}
	if err := runWriteBack(v, &pending.Operator, &pending.Subject, opts, pending.Body, pending.Outcome); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, "writeback.json"))
}
