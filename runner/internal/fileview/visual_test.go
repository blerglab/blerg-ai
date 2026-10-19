package fileview

import "testing"

func TestDiagramSourceIsText(t *testing.T) {
	for _, name := range []string{"diagram.mmd", "diagram.MERMAID"} {
		ct, view := Classify(name, []byte("flowchart LR\nA --> B"), MaxSniffText+1)
		if ct != "text/plain; charset=utf-8" || view != Text {
			t.Errorf("%s: %s/%s, want plain text", name, ct, view)
		}
	}
}
