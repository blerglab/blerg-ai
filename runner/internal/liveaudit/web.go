package liveaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func webObservation(data Object, exit int, seconds float64) Object {
	failed := []string{}
	assertion := false
	runtime := []string{}
	for _, test := range rows(data, "testResults") {
		for _, a := range rows(test, "assertionResults") {
			if str(a, "status") != "failed" {
				continue
			}
			failed = append(failed, str(a, "fullName"))
			for _, message := range stringsOf(a["failureMessages"]) {
				assertion = assertion || strings.Contains(message, "AssertionError") || strings.Contains(message, "expect(element)")
				runtime = append(runtime, matches(`\b(TypeError|ReferenceError|SyntaxError|RangeError):`, message)...)
			}
		}
	}
	return Object{"exit_code": exit, "failed_tests": failed, "assertion_failure": assertion, "runtime_error_types": unique(runtime), "passed": data["numPassedTests"], "pending": data["numPendingTests"], "runtime_errors": data["numRuntimeErrorTestSuites"], "seconds": seconds}
}
func webOutcome(observed Object) string {
	if num(observed, "exit_code") == 0 {
		return "survived"
	}
	if observed["assertion_failure"] == true && len(stringsOf(observed["runtime_error_types"])) == 0 {
		return "killed"
	}
	return "harness_or_runtime_error"
}
func Web(o Options) (int, error) {
	guards, e := WebCatalog(o.Root)
	if e != nil {
		return 2, e
	}
	guards, e = selectGuards(guards, o.Filter, o.IDsFile)
	if e != nil {
		return 2, e
	}
	base, _, _, e := command(o.Root, nil, 0, "git", "rev-parse", "HEAD")
	if e != nil {
		return 2, e
	}
	selection := "all catalogued client guards"
	if o.IDsFile != "" {
		selection = "explicit guard IDs"
	}
	record := Object{"base_commit": strings.TrimSpace(base), "method": "one source mutation per fresh Vitest process in private archive; flags unchanged", "selection": selection, "guards": []Object{}}
	if o.List {
		record["guards"] = guards
		fmt.Println("Catalogued", len(guards), "client guards")
		return 0, save(o.Output, record)
	}
	temp, e := os.MkdirTemp("", "blerg-web-mutations-")
	if e != nil {
		return 2, e
	}
	defer func() { _ = os.RemoveAll(temp) }()
	tree := filepath.Join(temp, "tree")
	if e = os.Mkdir(tree, 0700); e != nil {
		return 2, e
	}
	if e = archive(o.Root, tree); e != nil {
		return 2, e
	}
	if e = os.Symlink(filepath.Join(o.Root, "node_modules"), filepath.Join(tree, "node_modules")); e != nil {
		return 2, e
	}
	pkg, e := load(filepath.Join(o.Root, "package.json"))
	if e != nil {
		return 2, e
	}
	for _, pattern := range stringsOf(pkg["workspaces"]) {
		paths, _ := filepath.Glob(filepath.Join(o.Root, pattern))
		for _, w := range paths {
			rel, _ := filepath.Rel(o.Root, w)
			installed := filepath.Join(w, "node_modules")
			dest := filepath.Join(tree, rel, "node_modules")
			if _, e = os.Stat(installed); e == nil {
				if _, e = os.Stat(dest); os.IsNotExist(e) {
					if e = os.Symlink(installed, dest); e != nil {
						return 2, e
					}
				}
			}
		}
	}
	originals := map[string]string{}
	for _, g := range guards {
		file := str(g, "file")
		raw, e := source(o.Root, file)
		if e != nil {
			return 2, e
		}
		archived, e := source(tree, file)
		if e != nil {
			return 2, e
		}
		if raw != archived {
			return 2, fmt.Errorf("commit selected source before client audit: %s", file)
		}
		originals[file] = raw
	}
	report := filepath.Join(temp, "vitest.json")
	args := []string{"exec", "--workspace", "packages/chat", "--", "vitest", "run"}
	args = append(args, webTests...)
	args = append(args, "--reporter=json", "--outputFile="+report)
	run := func() (Object, bool, error) {
		if e := os.Remove(report); e != nil && !os.IsNotExist(e) {
			return nil, false, e
		}
		started := time.Now()
		_, exit, timed, e := command(tree, os.Environ(), 90*time.Second, "npm", args...)
		if e != nil {
			return nil, false, e
		}
		data := Object{}
		if b, err := os.ReadFile(report); err == nil { //nolint:gosec // Fixed report name in this freshly created private directory.
			if e = json.Unmarshal(b, &data); e != nil {
				return nil, false, e
			}
		}
		return webObservation(data, exit, rounded(time.Since(started))), timed, nil
	}
	baseline, timed, e := run()
	if e != nil {
		return 2, e
	}
	record["baseline"] = baseline
	if num(baseline, "exit_code") != 0 || timed {
		record["error"] = "private archive baseline failed"
		return 2, save(o.Output, record)
	}
	fmt.Println("BASELINE client PASS", baseline["passed"], "tests")
	observed := []Object{}
	for i, g := range guards {
		file := str(g, "file")
		raw := originals[file]
		a, b := num(g, "start"), num(g, "end")
		if a < 0 || b > len(raw) || raw[a:b] != str(g, "expression") {
			return 2, fmt.Errorf("client source span mismatch")
		}
		path := filepath.Join(tree, file)
		if e = os.WriteFile(path, []byte(raw[:a]+str(g, "replacement")+raw[b:]), 0600); e != nil {
			return 2, e
		}
		obs, timed, runErr := run()
		restoreErr := os.WriteFile(path, []byte(raw), 0600)
		if runErr != nil {
			return 2, runErr
		}
		if restoreErr != nil {
			return 2, restoreErr
		}
		row := clone(g)
		if timed {
			row["status"] = "timeout"
		} else {
			for k, v := range obs {
				row[k] = v
			}
			row["status"] = webOutcome(obs)
		}
		observed = append(observed, row)
		record["guards"] = observed
		if e = save(o.Output, record); e != nil {
			return 2, e
		}
		fmt.Println(i+1, "/", len(guards), g["id"], row["status"])
	}
	restored, timed, e := run()
	if e != nil {
		return 2, e
	}
	record["restored"] = restored
	record["summary"] = summary(observed)
	if e = save(o.Output, record); e != nil {
		return 2, e
	}
	fmt.Println(record["summary"])
	if timed || num(restored, "exit_code") != 0 {
		return 1, nil
	}
	for _, g := range observed {
		if str(g, "status") != "killed" {
			return 1, nil
		}
	}
	return 0, nil
}
