package daemon

import (
	"strings"
	"testing"
)

func TestReviewRoundPromptsDescribeBatchReplies(t *testing.T) {
	for name, prompt := range map[string]string{"native": harnessInstructions, "external": ccSessionGuide, "managed CLAUDE.md": managedBlock} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(prompt, "whole round in one call") || !strings.Contains(prompt, "--file") {
				t.Fatal("session prompt still describes only single replies")
			}
		})
	}
}
