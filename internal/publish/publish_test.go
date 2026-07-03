package publish

import (
	"strings"
	"testing"
)

func TestSummaryHTMLSanitizesAndTruncates(t *testing.T) {
	raw := "<p>" + strings.Repeat("word ", 140) + "</p><script>alert(1)</script>"
	got := SummaryHTML(raw)
	if strings.Contains(got, "script") {
		t.Fatalf("summary not sanitized: %s", got)
	}
	if !strings.HasPrefix(got, "<p>") || !strings.HasSuffix(got, "</p>") {
		t.Fatalf("summary is not wrapped in one paragraph: %s", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("summary was not truncated with ellipsis: %s", got)
	}
}
