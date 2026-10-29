package acceptance

import (
	"encoding/json"
	"errors"
	"strings"
)

// Text is a model-written free-text field. Reviewers sometimes return a list
// of strings where the schema asks for one string; both mean the same thing,
// so a string array is accepted and joined instead of failing the review.
type Text string

func (t *Text) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err == nil {
		if s != nil {
			*t = Text(*s)
		}
		return nil
	}
	var parts []string
	if err := json.Unmarshal(b, &parts); err != nil {
		return errors.New("text field must be a string or an array of strings")
	}
	kept := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	*t = Text(strings.Join(kept, "\n"))
	return nil
}
