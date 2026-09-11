package operator

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Salvaging a marker-less evaluation body (issue #307)
// -----------------------------------------------------
// The no-marker guard in Run exists to discard the short meta-summary a model
// leaves on stdout after self-posting the real body via a tool call (#143).
// But it keys off a single signal — "is there a marker?" — to answer a
// different question: "is this output usable?". Those two diverge in one
// observed failure mode: evaluate-bug / evaluate-feat print the entire
// template, footer included, and stop one line short of the marker. The guard
// then threw away 4000+ character evaluations complete with dimension scores
// and a Confidence line, charged the run, and let the issue re-fire for a full
// second payment on the next pass (8 recorded occurrences, ~$12.49).
//
// salvageOutcome closes that gap by matching the *template skeleton* instead of
// the marker: a Confidence line plus at least two of the operator's own
// dimension score lines. That is a far stronger signal of "the model completed
// its job" than output length (observed bodies range 2732–7185 chars while a
// self-post summary can be 12–105 chars, with no clean cutoff between them).
// Short-summary output matches nothing here and keeps the existing discard
// behaviour.

// confidenceRE extracts the numeric Confidence score from an evaluation body.
// Only the number is read: the trailing annotation is free-form and observed in
// both English ("✅ above threshold") and Chinese ("✅ 高于阈值"), so matching it
// would make salvage locale-dependent.
var confidenceRE = regexp.MustCompile(`\*\*Confidence:\*\*\s*([0-9]+(?:\.[0-9]+)?)\s*/\s*10`)

// evalDimensions maps an evaluation operator to the three scoring dimensions
// its SKILL.md template emits. The names differ per operator, so a single
// hardcoded set would silently fail to salvage the other one.
var evalDimensions = map[string][]string{
	"evaluate-bug":  {"Reproducibility", "Root cause", "Fix difficulty"},
	"evaluate-feat": {"Clarity", "Scope", "Architecture fit"},
}

// evalConfidenceThreshold mirrors the "Threshold = 7.0" line in both evaluate
// SKILL.md templates: at or above it the operator declares agent-evaluated,
// below it agent-skipped.
const evalConfidenceThreshold = 7.0

// salvageOutcome inspects a marker-less operator body and reports the outcome
// label it should have declared, when the body is recognisably a complete
// evaluation. ok is false for anything else — including operators that are not
// evaluators — in which case the caller keeps the existing no-marker discard.
func salvageOutcome(op *Operator, body string) (outcome string, confidence float64, ok bool) {
	dims, isEval := evalDimensions[op.Name]
	if !isEval {
		return "", 0, false
	}
	// Guard against a skill that reuses an evaluate-* name but declares
	// different terminal labels: salvage may only pick labels the operator
	// itself is allowed to apply.
	if !outcomeAllowed(op, "agent-evaluated") || !outcomeAllowed(op, "agent-skipped") {
		return "", 0, false
	}

	scores := dimensionScores(body, dims)

	// Require at least two of the three dimension lines. Demanding all three
	// would lose a body whose last dimension got reworded; requiring only one
	// would risk matching prose that merely quotes a score.
	if len(scores) < 2 {
		return "", 0, false
	}

	conf, ok := confidenceScore(body)
	if !ok {
		// Some bodies drop the Confidence line along with the marker (observed
		// on clawflow#308). Both SKILL.md templates define "Confidence =
		// average of the three", so recompute it — but only from a full set of
		// three, since averaging a partial set would understate or overstate
		// the real score.
		if len(scores) != len(dims) {
			return "", 0, false
		}
		conf = mean(scores)
	}

	if conf >= evalConfidenceThreshold {
		return "agent-evaluated", conf, true
	}
	return "agent-skipped", conf, true
}

// confidenceScore reads the explicit Confidence score from body.
func confidenceScore(body string) (float64, bool) {
	m := confidenceRE.FindStringSubmatch(body)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// dimensionScores returns the scores of whichever of `dims` appear as a
// "**Name:** N/10" line in body, in template order.
func dimensionScores(body string, dims []string) []float64 {
	var out []float64
	for _, d := range dims {
		re := regexp.MustCompile(`\*\*` + regexp.QuoteMeta(d) + `:\*\*\s*([0-9]+(?:\.[0-9]+)?)\s*/\s*10`)
		m := re.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// mean averages a non-empty score set, rounded to one decimal so the notice
// prints the same shape the template would have ("9.3/10", not "9.333…/10").
func mean(vals []float64) float64 {
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return math.Round(sum/float64(len(vals))*10) / 10
}

// salvageNotice is the banner prepended to a salvaged comment so the issue
// thread records that the label was inferred rather than declared.
func salvageNotice(outcome string, confidence float64) string {
	return fmt.Sprintf(
		"> ⚠️ 本次运行未产出 outcome marker，标签 `%s` 由正文的 **Confidence: %s/10** 与 7.0 阈值推断（issue #307）。",
		outcome, strconv.FormatFloat(confidence, 'f', -1, 64),
	)
}

// prependSalvageNotice puts the notice above the salvaged body.
func prependSalvageNotice(body, outcome string, confidence float64) string {
	return salvageNotice(outcome, confidence) + "\n\n" + strings.TrimSpace(body)
}
