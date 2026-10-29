package acceptance

import "errors"

// DecodeUpload applies the same exact schema checks as persisted evidence.
func DecodeUpload(raw []byte, r *Record) error { return decodeStrict(raw, r) }

// DecodeExact rejects ambiguous schema fields in evidence transport envelopes.
func DecodeExact(raw []byte, value any) error { return decodeStrict(raw, value) }

// ValidateUpload preserves identity checks for all reviewer judgements.
// Only explicitly marked unstructured failures may omit report-shape evidence.
// Infrastructure records can only consume retry budget, never open the gate.
func ValidateUpload(c Contract, r Record) error {
	d, err := c.Digest()
	actual, e := r.Contract.Digest()
	if err != nil || e != nil || d != actual || d != r.ContractDigest || r.Version != Version || r.Role != "acceptance" {
		return errors.New("upload contract mismatch")
	}
	if err := completeRecord(r); err != nil {
		return err
	}
	if IsInfrastructure(r) {
		if r.Verdict != Blocked || r.Cancelled {
			return errors.New("invalid infrastructure record")
		}
		if r.Plan.Text != "" {
			if r.Plan.AttemptID != r.AttemptID {
				return errors.New("upload plan attempt mismatch")
			}
			return r.Plan.validate(c)
		}
		return nil
	}
	if r.Verdict != Passed && r.Verdict != Failed && r.Verdict != Blocked {
		return errors.New("invalid verdict")
	}
	if _, err := validateFindings(r.Findings); err != nil {
		return err
	}
	if !r.Unstructured {
		if err := ValidateChecks(c, r); err != nil {
			return err
		}
	}
	checked := r
	checked.Verdict, checked.Findings = Passed, nil
	checked.Unstructured, checked.ValidationError = false, ""
	return evaluate(c, checked, false, !r.Unstructured)
}
