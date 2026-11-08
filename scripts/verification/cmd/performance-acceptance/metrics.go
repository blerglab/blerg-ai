package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"html"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var routeNames = []string{"TestGetLiveOnALongSession", "TestGetTopicsOnALongSession", "TestLivePollingRetainedHeap", "TestInitialStateSeededConnect", "TestEmitterAckReleasesPayloads"}
var scenarioNames = []string{"cold-load", "transcript-open", "typing", "insights-7d", "board-tab", "workspace-switch", "search", "blackboard"}
var requiredMetrics = []string{"WORKSPACE_SWITCH", "MARKDOWN_SWITCH", "TAB_OPEN_BOARD_INSIGHTS", "FIRST_LOAD_GRAPH", "TAB_SWITCH", "WORKSPACE_POLLING"}
var metricNames = []string{"WORKSPACE_NATIVE", "WORKSPACE_TYPING", "WORKSPACE_SWITCH", "MARKDOWN_SWITCH", "TAB_OPEN_BOARD_INSIGHTS", "FIRST_LOAD_GRAPH", "WORKSPACE_POLLING", "BOARD_TELEMETRY"}

func obj(m map[string]any, k string) map[string]any { v, _ := m[k].(map[string]any); return v }
func array(m map[string]any, k string) []any        { v, _ := m[k].([]any); return v }
func number(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return math.NaN()
	}
}
func mapping(v any) map[string]any { m, _ := v.(map[string]any); return m }
func finite(v float64) bool        { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func extractMetrics(text string) map[string][]any {
	found := map[string][]any{}
	add := func(name string, v any) {
		for _, old := range found[name] {
			if reflect.DeepEqual(v, old) {
				return
			}
		}
		found[name] = append(found[name], v)
	}
	tab := regexp.MustCompile(`(\{"width":\d+,"commitMedianMs":.*\})`)
	for _, line := range strings.Split(text, "\n") {
		for _, name := range metricNames {
			i := strings.Index(line, name+" ")
			if i < 0 {
				continue
			}
			raw := line[i+len(name)+1:]
			end := strings.LastIndex(raw, "}")
			if end < 0 {
				continue
			}
			var v map[string]any
			if json.Unmarshal([]byte(raw[:end+1]), &v) != nil || v == nil {
				continue
			}
			for _, key := range []string{"counts", "keyRenders", "eventRenders"} {
				delete(v, key)
			}
			add(name, v)
		}
		if match := tab.FindString(line); match != "" {
			var v map[string]any
			if json.Unmarshal([]byte(match), &v) == nil {
				add("TAB_SWITCH", v)
			}
		}
	}
	return found
}
func assess(r map[string]any, typing, boot, bundle float64) []string {
	failures := []string{}
	for _, name := range routeNames {
		if obj(r, "routes")[name] != "pass" {
			failures = append(failures, "missing_or_skipped_route_tests")
			break
		}
	}
	runs := array(r, "production_scenarios")
	valid := len(runs) == 3
	for _, run := range runs {
		names := map[string]bool{}
		for _, item := range array(mapping(run), "scenarios") {
			m := mapping(item)
			name, _ := m["name"].(string)
			if len(array(m, "clientSamplesMs")) > 0 {
				names[name] = true
			}
		}
		if len(names) != len(scenarioNames) {
			valid = false
		}
		for _, n := range scenarioNames {
			if !names[n] {
				valid = false
			}
		}
	}
	if !valid {
		failures = append(failures, "missing_production_scenarios")
	}
	markValid, markExceeded := productionTypingMark(runs)
	if !markValid {
		failures = append(failures, "production:missing_or_invalid_typing_mark")
	}
	if markExceeded {
		failures = append(failures, "production:typing_mark_budget")
	}
	for _, phase := range []string{"quiet", "loaded"} {
		metrics := obj(obj(obj(r, "steps"), phase), "metrics")
		widths := map[float64]bool{}
		bad := false
		for _, item := range array(metrics, "WORKSPACE_NATIVE") {
			m := mapping(item)
			widths[number(m, "width")] = true
			v := number(m, "paintMedianMs")
			if !finite(v) || v > typing {
				bad = true
			}
		}
		if len(widths) != 2 || !widths[390] || !widths[3840] {
			failures = append(failures, phase+":missing_native_input")
		}
		if bad {
			failures = append(failures, phase+":typing_budget")
		}
		for _, name := range requiredMetrics {
			if len(array(metrics, name)) == 0 {
				failures = append(failures, phase+":missing_"+strings.ToLower(name))
			}
		}
	}
	size := number(obj(r, "bundle"), "entry_js_gzip_bytes")
	if !finite(size) || size > bundle {
		failures = append(failures, "bundle_budget_or_missing_bundle")
	}
	prod := obj(r, "production")
	comparison := compareBoot(obj(prod, "boot"), obj(r, "boot_baseline"), boot)
	if comparison["valid"] != true {
		failures = append(failures, "missing_or_invalid_boot_samples")
	}
	if comparison["regressed"] == true {
		failures = append(failures, "boot_regression_vs_baseline")
	}

	if prod["status"] != "complete" {
		failures = append(failures, "production_or_soak_incomplete")
	}
	soak := obj(prod, "soak")
	duration := number(soak, "durationSeconds")
	if !finite(duration) || duration < math.Max(1800, number(obj(r, "config"), "soak_seconds")) {
		failures = append(failures, "soak_duration")
	}
	if _, ok := obj(soak, "processes")["daemon"]; !ok {
		failures = append(failures, "missing_daemon_samples")
	}
	if len(array(soak, "samples")) < 3 {
		failures = append(failures, "insufficient_soak_samples")
	}
	for name, v := range obj(soak, "processes") {
		if number(mapping(v), "delta") > 64*1024*1024 {
			failures = append(failures, "soak_rss_growth_"+name)
		}
	}
	if number(obj(soak, "browserHeap"), "delta") > 16*1024*1024 {
		failures = append(failures, "soak_browser_heap_growth")
	}
	if len(obj(soak, "requestFamilies")) == 0 {
		failures = append(failures, "missing_production_polling_evidence")
	}
	for name, v := range obj(r, "steps") {
		if number(mapping(v), "exit_code") != 0 {
			failures = append(failures, name+":process_exit")
		}
	}
	sort.Strings(failures)
	return failures
}

// Use the same nearest-rank p95 and strict ceiling as release-perf. Check
// every calibration, including a first run with no official release baseline.
func productionTypingMark(runs []any) (valid, exceeded bool) {
	valid = len(runs) == 3
	for _, run := range runs {
		found := false
		for _, item := range array(mapping(run), "scenarios") {
			m := mapping(item)
			if m["name"] != "typing" {
				continue
			}
			found = true
			values := []float64{}
			for _, value := range array(m, "clientSamplesMs") {
				n, ok := value.(float64)
				if !ok || !finite(n) || n < 0 {
					valid = false
					continue
				}
				values = append(values, n)
			}
			if len(values) == 0 {
				valid = false
				continue
			}
			sort.Float64s(values)
			if values[int(math.Ceil(.95*float64(len(values))))-1] >= 16 {
				exceeded = true
			}
		}
		if !found {
			valid = false
		}
	}
	return valid, exceeded
}

var tags = regexp.MustCompile(`(?is)<(script|link)\b([^>]*)>`)
var attributes = regexp.MustCompile(`([A-Za-z]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

func bundleSize(dist string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(dist, "index.html")) //nolint:gosec // G304: production build output within this run's evidence directory.
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, tag := range tags.FindAllStringSubmatch(string(data), -1) {
		attrs := map[string]string{}
		for _, a := range attributes.FindAllStringSubmatch(tag[2], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
		}
		if strings.EqualFold(tag[1], "script") && attrs["src"] != "" {
			names[attrs["src"]] = true
		}
		if strings.EqualFold(tag[1], "link") && (attrs["rel"] == "modulepreload" || attrs["rel"] == "stylesheet") {
			names[attrs["href"]] = true
		}
	}
	keys := []string{}
	for n := range names {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	root, err := filepath.EvalSymlinks(dist)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	assets := []any{}
	total := 0
	js := false
	for _, name := range keys {
		path, err := filepath.EvalSymlinks(filepath.Join(root, strings.TrimLeft(name, "/")))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, errors.New("bundle_asset_outside_dist")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var compressed bytes.Buffer
		w, err := gzip.NewWriterLevel(&compressed, 6)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(raw); err != nil {
			return nil, err
		}
		if err = w.Close(); err != nil {
			return nil, err
		}
		javascript := filepath.Ext(path) == ".js"
		if javascript {
			total += compressed.Len()
			js = true
		}
		assets = append(assets, map[string]any{"name": filepath.Base(path), "raw_bytes": len(raw), "gzip_bytes": compressed.Len(), "javascript": javascript})
	}
	if !js {
		return nil, errors.New("missing_entry_javascript")
	}
	return map[string]any{"assets": assets, "entry_js_gzip_bytes": total, "compression": "Go gzip level 6, mtime=0; HTML entry/modulepreload assets"}, nil
}
func productionScenarios(directory string) ([]any, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "scenarios-*.json"))
	if err != nil {
		return nil, err
	}
	runs := []any{}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is a fixed-pattern scenario file in private evidence.
		if err != nil {
			return nil, err
		}
		var raw map[string]any
		if err = json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		scenarios := []any{}
		for _, v := range array(raw, "scenarios") {
			m := mapping(v)
			name, _ := m["name"].(string)
			if !slices.Contains(scenarioNames, name) {
				return nil, errors.New("unexpected_production_scenario")
			}
			item := map[string]any{"name": name}
			keys := []string{"clientSamplesMs", "serverSamplesMs"}
			for _, key := range []string{"preKeydownSamplesMs", "commandToCommitSamplesMs"} {
				if raw, present := m[key]; present {
					values, ok := raw.([]any)
					if !ok || len(values) != len(array(m, "clientSamplesMs")) {
						return nil, errors.New("invalid_production_typing_diagnostics")
					}
					keys = append(keys, key)
				}
			}
			for _, key := range keys {
				values := array(m, key)
				if values == nil {
					values = []any{}
				}
				for _, v := range values {
					n, ok := v.(float64)
					if !ok || !finite(n) || n < 0 {
						return nil, errors.New("invalid_production_timing")
					}
				}
				item[key] = values
			}
			scenarios = append(scenarios, item)
		}
		runs = append(runs, map[string]any{"file": filepath.Base(path), "scenarios": scenarios})
	}
	return runs, nil
}

// The 3 s number is an informational target. Only an observed slower median
// against the explicit same-machine historical baseline is a regression gate.
func compareBoot(current, baseline map[string]any, target float64) map[string]any {
	stats := func(m map[string]any) (float64, float64, bool) {
		samples := array(m, "samplesMs")
		if len(samples) != 3 {
			return 0, 0, false
		}
		values := []float64{}
		for _, v := range samples {
			n, ok := v.(float64)
			if !ok || !finite(n) || n <= 0 {
				return 0, 0, false
			}
			values = append(values, n)
		}
		sort.Float64s(values)
		return values[1], values[2], true
	}
	median, maximum, valid := stats(current)
	result := map[string]any{"valid": valid, "target_ms": target, "target_met": valid && maximum <= target, "regressed": false, "gate": "none_without_measured_baseline"}
	if valid {
		result["median_ms"] = median
		result["maximum_ms"] = maximum
	}
	if before, _, ok := stats(baseline); ok {
		result["baseline_median_ms"] = before
		result["gate"] = "measured_median_vs_baseline"
		if valid {
			result["delta_ms"] = median - before
			result["regressed"] = median > before
		}
	}
	return result
}
