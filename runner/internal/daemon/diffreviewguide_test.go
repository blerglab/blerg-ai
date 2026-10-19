package daemon

import (
	"strings"
	"testing"
)

func TestGuideExplainsDiffReview(t *testing.T) {
	for name, guide := range map[string]string{"native": ccSessionGuide, "managed": managedBlock} {
		if !strings.Contains(guide, "source files") || !strings.Contains(guide, "updated diff") || !strings.Contains(guide, "review reply <id>") {
			t.Errorf("%s guide lacks source-file diff review loop", name)
		}
	}
}
