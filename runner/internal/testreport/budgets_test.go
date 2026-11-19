package testreport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBudgetsExplicitLayersAndBounds(t *testing.T) {
	for _, layer := range []string{"1", "2", "3a", "3b", "4", "5"} {
		raw := fmt.Sprintf(`{"version":1,"suites":{"unit":{"layer":%q,"budget_seconds":10}}}`, layer)
		got, err := ReadBudgets(strings.NewReader(raw))
		if err != nil || got["unit"].Layer != layer || *got["unit"].Seconds != 10 {
			t.Fatalf("layer %s=%+v %v", layer, got, err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"version":1,"suites":null}`, `{"version":1,"suites":{"unit":{"layer":"2"}}}`, `{"version":1,"suites":{"bad name":{"layer":"2","budget_seconds":10}}}`, `{"version":1,"suites":{"unit":{"layer":"2","budget_seconds":604801}}}`, `{"version":1,"suites":{}} {}`, strings.Repeat(" ", 64<<10+1)} {
		if _, err := ReadBudgets(strings.NewReader(raw)); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	suites := map[string]any{}
	for i := range 129 {
		suites[fmt.Sprintf("suite%d", i)] = map[string]any{"layer": "2", "budget_seconds": 10}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "suites": suites})
	if _, err := ReadBudgets(strings.NewReader(string(raw))); err == nil {
		t.Fatal("too many suites accepted")
	}
	if got, err := ReadBudgets(strings.NewReader(`{"version":1,"suites":{}}`)); err != nil || len(got) != 0 {
		t.Fatal("explicit empty configuration refused")
	}
}
