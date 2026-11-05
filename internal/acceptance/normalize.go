package acceptance

import "strings"

// MaxUnstructuredRetries bounds fresh same-head acceptance runs after a report
// that stays invalid following normalization and the one allowed correction.
// These retries never consume the repair ledger.
const MaxUnstructuredRetries = 2

var knownNextOwners = map[string]bool{
	"implementer": true, "implementation": true, "owner": true,
	"runner": true, "acceptance": true, "environment": true, "human": true,
}

// normalizeMissingEvidence deterministically downgrades failing requirement or
// defect findings that carry no failed observation when every checklist item
// for their requirement is blocked with a stated gap and no failed evidence.
// Missing execution evidence is a gap, not a demonstrated defect. It never
// produces a passed verdict: failed becomes blocked only when no failing
// finding and no failed check remain.
func normalizeMissingEvidence(criteria []Criterion, checks []RequirementCheck, findings []Finding, status Verdict, owner, action string) ([]Finding, Verdict, string, string, bool) {
	req := map[string]string{}
	for _, c := range criteria {
		req[c.ID] = c.Requirement
	}
	out := append([]Finding(nil), findings...)
	changed := false
	for i, f := range out {
		if f.Kind != "requirement" && f.Kind != "defect" {
			continue
		}
		if o := f.Observation; o != nil && o.Result == "failed" {
			continue
		}
		if strings.TrimSpace(f.Requirement) == "" {
			continue
		}
		matched, ok := 0, true
		for _, ch := range checks {
			if req[ch.ID] != f.Requirement {
				continue
			}
			matched++
			if ch.Status != Blocked || !nonempty(string(ch.Missing)) {
				ok = false
			}
			for _, e := range ch.Evidence {
				if e.Result == "failed" {
					ok = false
				}
			}
		}
		if matched == 0 || !ok {
			continue
		}
		out[i].Kind = "advisory"
		changed = true
	}
	if !changed {
		return findings, status, owner, action, false
	}
	if status == Failed {
		stillFailing := false
		for _, f := range out {
			stillFailing = stillFailing || f.Failing()
		}
		for _, ch := range checks {
			stillFailing = stillFailing || ch.Status == Failed
		}
		if !stillFailing {
			status = Blocked
		}
	}
	if !knownNextOwners[strings.ToLower(strings.TrimSpace(owner))] {
		owner = "implementer"
	}
	if !nonempty(action) {
		action = "Provide the missing execution evidence, then run fresh acceptance"
	}
	return out, status, owner, action, true
}

// TrailingUnstructured counts the newest consecutive completed records that
// ended as runner-owned unstructured findings.
func TrailingUnstructured(records []Record) int {
	n := 0
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if !r.Completed {
			continue
		}
		if !r.Unstructured {
			break
		}
		n++
	}
	return n
}

// UnstructuredAttempts reads the trailing unstructured count at the contract's
// head. Records written before this policy count as well.
func (s *Store) UnstructuredAttempts(c Contract) (int, error) {
	records, err := s.RecordsAtHead(c.Repository, c.PRNumber, c.HeadSHA)
	if err != nil {
		return 0, err
	}
	var complete []Record
	for _, r := range records {
		if r.Completed {
			complete = append(complete, r)
		}
	}
	if len(complete) > 0 && complete[len(complete)-1].Contract.Requirements != c.Requirements {
		return 0, nil
	}
	return TrailingUnstructured(complete), nil
}
