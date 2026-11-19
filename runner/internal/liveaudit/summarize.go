package liveaudit

import (
	"fmt"
	"sort"
)

func validateRun(data Object) error {
	if str(data, "error") != "" {
		return fmt.Errorf("failed baseline is not proof")
	}
	for _, r := range rows(data, "baselines") {
		if num(r, "exit_code") != 0 {
			return fmt.Errorf("baseline refusal")
		}
	}
	for _, k := range []string{"baseline", "restored"} {
		if r, ok := data[k].(map[string]any); ok && num(r, "exit_code") != 0 {
			return fmt.Errorf("%s baseline refusal", k)
		}
	}
	return nil
}
func Summarize(root string, catalogs, records []string, output string) (int, error) {
	expected := map[string]Object{}
	for _, name := range catalogs {
		data, e := load(name)
		if e != nil {
			return 2, e
		}
		for _, g := range rows(data, "guards") {
			raw, e := source(root, str(g, "file"))
			if e != nil {
				return 2, e
			}
			if digest([]byte(raw)) != str(g, "source_sha256") {
				return 2, fmt.Errorf("stale source catalogue: %s", str(g, "file"))
			}
			if expected[str(g, "id")] != nil {
				return 2, fmt.Errorf("duplicate current guard ID")
			}
			expected[str(g, "id")] = g
		}
	}
	histories := map[string][]Object{}
	runs := []Object{}
	for _, name := range records {
		data, e := load(name)
		if e != nil {
			return 2, e
		}
		if e = validateRun(data); e != nil {
			return 2, e
		}
		baseline := data["baselines"]
		if baseline == nil {
			baseline = data["baseline"]
		}
		selection := data["selection"]
		if selection == nil {
			selection = "all indexed guards"
		}
		runs = append(runs, Object{"base_commit": data["base_commit"], "method": data["method"], "selection": selection, "baselines": baseline, "restored": data["restored"]})
		for _, g := range rows(data, "guards") {
			id := str(g, "id")
			want := expected[id]
			if want == nil || g["file"] != want["file"] || g["source_sha256"] != want["source_sha256"] {
				continue
			}
			r := clone(g)
			r["proof_commit"] = data["base_commit"]
			r["run_index"] = len(runs) - 1
			histories[id] = append(histories[id], r)
		}
	}
	out := []Object{}
	missing := false
	for id, g := range expected {
		h := histories[id]
		var r Object
		if len(h) > 0 {
			r = clone(h[len(h)-1])
			earlier := []Object{}
			for _, old := range h[:len(h)-1] {
				earlier = append(earlier, Object{"status": old["status"], "proof_commit": old["proof_commit"], "suite": old["suite"]})
			}
			r["earlier_observations"] = earlier
		} else {
			r = clone(g)
			r["status"] = "not_recorded"
			missing = true
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a["file"] != b["file"] {
			return str(a, "file") < str(b, "file")
		}
		if num(a, "start") != num(b, "start") {
			return num(a, "start") < num(b, "start")
		}
		return str(a, "id") < str(b, "id")
	})
	result := Object{"scope": "mechanically indexed conditions and explicitly named side effects; source completeness and survivor classification still require review", "runs": runs, "summary": summary(out), "guards": out}
	if e := save(output, result); e != nil {
		return 2, e
	}
	fmt.Println(result["summary"])
	if missing {
		return 1, nil
	}
	return 0, nil
}
