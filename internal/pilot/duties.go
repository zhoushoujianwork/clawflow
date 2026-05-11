package pilot

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// dutiesBlockRE matches the fenced YAML duties block emitted by Pilot.
// It captures the YAML body. `(?s)` lets `.` match newlines so a multi-
// line block is one match. Picking the LAST occurrence (FindAll +
// last index) so a Pilot that streams a draft block before the final
// block doesn't trip us up.
var dutiesBlockRE = regexp.MustCompile("(?s)```pilot-duties\\s*\\n(.*?)\\n```")

// dutiesYAML is the on-the-wire shape Pilot writes. Mirrors
// snapshot.PilotDuties / snapshot.PilotDuty but separated so the
// schema Pilot has to honour can evolve independently from the
// dashboard's internal type.
type dutiesYAML struct {
	Duties struct {
		PRTriage struct {
			Status  string   `yaml:"status"`
			Actions []string `yaml:"actions,omitempty"`
			Note    string   `yaml:"note,omitempty"`
		} `yaml:"pr_triage"`
		Monitoring struct {
			Status  string   `yaml:"status"`
			Actions []string `yaml:"actions,omitempty"`
			Note    string   `yaml:"note,omitempty"`
		} `yaml:"monitoring"`
		DocSync struct {
			Status  string   `yaml:"status"`
			Actions []string `yaml:"actions,omitempty"`
			Note    string   `yaml:"note,omitempty"`
		} `yaml:"doc_sync"`
		IssueDigest struct {
			Summary string `yaml:"summary"`
		} `yaml:"issue_digest"`
		BacklogHygiene struct {
			Status  string   `yaml:"status"`
			Actions []string `yaml:"actions,omitempty"`
			Note    string   `yaml:"note,omitempty"`
		} `yaml:"backlog_hygiene"`
	} `yaml:"duties"`
}

// ExtractDuties parses Pilot's stdout for the fenced ```pilot-duties
// YAML block and returns the structured duty rollup. Returns nil when
// the block is absent or malformed — the caller is expected to fall
// back to the free-form Summary in that case (keeps legacy / failed
// runs displayable).
func ExtractDuties(output string) *snapshot.PilotDuties {
	matches := dutiesBlockRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	// Take the last block — if Pilot drafted earlier in the stream
	// then wrote a final one, the final one wins.
	body := matches[len(matches)-1][1]

	var parsed dutiesYAML
	if err := yaml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}

	// Normalize status fields. Pilot is asked to use a fixed
	// vocabulary; anything outside it gets coerced to "ok" with the
	// raw value preserved in Note so a post-mortem can see the
	// drift. This avoids a single typo silently downgrading the
	// dashboard to "no duties".
	normStatus := func(s, note string) (string, string) {
		s = strings.TrimSpace(strings.ToLower(s))
		switch s {
		case "ok", "action_taken", "flagged", "error":
			return s, note
		case "":
			return "ok", note
		default:
			combined := "unknown status " + s
			if note != "" {
				combined = combined + " — " + note
			}
			return "ok", combined
		}
	}

	prStatus, prNote := normStatus(parsed.Duties.PRTriage.Status, parsed.Duties.PRTriage.Note)
	moStatus, moNote := normStatus(parsed.Duties.Monitoring.Status, parsed.Duties.Monitoring.Note)
	dsStatus, dsNote := normStatus(parsed.Duties.DocSync.Status, parsed.Duties.DocSync.Note)
	bhStatus, bhNote := normStatus(parsed.Duties.BacklogHygiene.Status, parsed.Duties.BacklogHygiene.Note)

	return &snapshot.PilotDuties{
		PRTriage:       snapshot.PilotDuty{Status: prStatus, Actions: parsed.Duties.PRTriage.Actions, Note: prNote},
		Monitoring:     snapshot.PilotDuty{Status: moStatus, Actions: parsed.Duties.Monitoring.Actions, Note: moNote},
		DocSync:        snapshot.PilotDuty{Status: dsStatus, Actions: parsed.Duties.DocSync.Actions, Note: dsNote},
		IssueDigest:    snapshot.PilotIssueDigest{Summary: strings.TrimSpace(parsed.Duties.IssueDigest.Summary)},
		BacklogHygiene: snapshot.PilotDuty{Status: bhStatus, Actions: parsed.Duties.BacklogHygiene.Actions, Note: bhNote},
	}
}

// StripDutiesBlock removes the duties YAML block from `output` so it
// doesn't render as raw fenced YAML inside the Summary card. Returns
// the cleaned text; the original is unchanged when no block matched.
func StripDutiesBlock(output string) string {
	return dutiesBlockRE.ReplaceAllString(output, "")
}
