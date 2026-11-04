package operator

import "testing"

const gatewayNotice = "\n\n---\n\n> **Security notice**\n>\n> Credentials were redacted.\n>\n> **Use references instead.**\n"

func TestParseOutcomeIgnoresGatewayNoticeAfterMarker(t *testing.T) {
	label, cleaned := parseOutcome("Done.\n\n<!-- clawflow:outcome=agent-implemented -->" + gatewayNotice)
	if label != "agent-implemented" {
		t.Fatalf("label = %q", label)
	}
	if cleaned != "Done." {
		t.Fatalf("cleaned = %q", cleaned)
	}
}

func TestParseOutcomeKeepsBodiesWithoutThatExactShape(t *testing.T) {
	for name, body := range map[string]string{
		"no marker before the rule":     "Done." + gatewayNotice,
		"prose between marker and rule": "<!-- clawflow:outcome=agent-implemented -->\nmore prose\nand more\nand more\nand more" + gatewayNotice,
		"text after the quote":          "<!-- clawflow:outcome=agent-implemented -->\n---\n> quote\nnot a quote\none\ntwo\nthree",
		"rule without a quote":          "<!-- clawflow:outcome=agent-implemented -->\n---\none\ntwo\nthree",
	} {
		if label, _ := parseOutcome(body); label != "" {
			t.Errorf("%s: label = %q, want none", name, label)
		}
	}
}

func TestParseOutcomeKeepsQuoteBeforeMarker(t *testing.T) {
	label, cleaned := parseOutcome("---\n> keep this quote\n\n<!-- clawflow:outcome=agent-implemented -->")
	if label != "agent-implemented" || cleaned != "---\n> keep this quote" {
		t.Fatalf("label = %q cleaned = %q", label, cleaned)
	}
}
