package acceptance

// correctionPrompt supplies the report schema without asking for new investigation.
func correctionPrompt(c Contract, validationError string) string {
	prompt := "Your report failed validation: " + validationError + "\nReturn the corrected full JSON report only; do not re-review. Do not run checks, edit source, change HEAD or workspace, or publish anything. Correct only the report using observations already collected. Do not invent evidence. " +
		"Required shape: one JSON object with status (passed|failed|blocked), summary (nonempty string), evidence (nonempty array of nonempty strings), findings (array). Findings: {kind: requirement|defect|advisory, summary, requirement, defect, evidence: string, locations: [{path: repository-relative, line: positive head-revision line, side: head}]}. Failing findings require concrete evidence and the violated requirement or defect; advisory findings cannot fail acceptance. "
	if c.EvidenceSchema == 1 {
		prompt += "Include checks: [{id: saved criterion ID, status: passed|failed|blocked, evidence: [{type: source|unit|integration|live, check, reference, commit, result: inspected|exists|passed|failed|unverified}], missing: string}], next_action and next_owner (nonempty strings). Cover each saved criterion once; evidence must bind the submitted head and satisfy its saved proof. Checks, findings and verdict must agree. Each failed check needs an actionable finding whose requirement exactly quotes that criterion. Blocked checks identify missing proof. Observation shape on each failing finding: {type: source|unit|integration|live, check, reference, commit: " + c.HeadSHA + ", result: failed}. The requirement must match its failed checklist criterion exactly. "
	} else {
		prompt += "Evidence must include an already executed command as `command: <exact command> => <result>`. "
	}
	return prompt
}
