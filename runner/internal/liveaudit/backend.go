package liveaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Root, Output, Go, Filter, IDsFile, TestRun string
	List                                       bool
}
type insertion struct {
	at   int
	text string
}

func insert(raw string, edits []insertion) string {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	for _, e := range edits {
		raw = raw[:e.at] + e.text + raw[e.at:]
	}
	return raw
}
func instrument(root, tree string, guards, all []Object) error {
	edits := map[string][]insertion{}
	spans := map[string]map[string]map[string][2]int{}
	switches := map[string]bool{}
	selected := map[string]bool{}
	switchKey := func(g Object) string {
		return str(g, "file") + ":" + strconv.Itoa(num(g, "tag_start")) + ":" + strconv.Itoa(num(g, "tag_end"))
	}
	for _, g := range guards {
		selected[str(g, "id")] = true
		if str(g, "kind") == "case" && str(g, "tag") != "" {
			switches[switchKey(g)] = true
		}
	}
	instrumentation := append([]Object{}, guards...)
	for _, g := range all {
		if str(g, "kind") == "case" && switches[switchKey(g)] && !selected[str(g, "id")] {
			instrumentation = append(instrumentation, g)
		}
	}
	rewritten := map[string]bool{}
	literals := map[string]bool{}
	replacements := map[string]string{}
	for _, g := range instrumentation {
		file, id, kind := str(g, "file"), str(g, "id"), str(g, "kind")
		before, after := "", ""
		if replacement, ok := g["replacement"].(string); ok {
			replacements[id] = replacement
		}
		switch kind {
		case "case":
			tag := str(g, "tag")
			if tag != "" {
				key := switchKey(g)
				if !rewritten[key] {
					edits[file] = append(edits[file], insertion{num(g, "tag_start"), "true/*"}, insertion{num(g, "tag_end"), "*/"})
					rewritten[key] = true
				}
				before = "(blergMutationGuard(" + strconv.Quote(id) + ") && (" + tag + " == ("
				after = ")))"
			} else {
				before = "(blergMutationGuard(" + strconv.Quote(id) + ") && ("
				after = "))"
			}
		case "statement":
			before = "if blergMutationGuard(" + strconv.Quote(id) + ") { "
			after = "; }"
		case "effect":
			before = "(CASE WHEN current_setting('blerg.mutation_guard',true)='" + id + "' THEN 0 ELSE ("
			after = ") END)"
		case "trigger":
			before = "(coalesce(current_setting('blerg.mutation_guard',true),'')<>'" + id + "' AND ("
			after = "))"
		case "sql":
			literal := file + ":" + strconv.Itoa(num(g, "literal_start"))
			pkg := filepath.ToSlash(filepath.Dir(file))
			if spans[pkg] == nil {
				spans[pkg] = map[string]map[string][2]int{}
			}
			if spans[pkg][literal] == nil {
				spans[pkg][literal] = map[string][2]int{}
			}
			spans[pkg][literal][id] = [2]int{num(g, "relative_start"), num(g, "relative_end")}
			if !literals[literal] {
				edits[file] = append(edits[file], insertion{num(g, "literal_start"), "blergMutationSQL(" + strconv.Quote(literal) + ","}, insertion{num(g, "literal_end"), ")"})
				literals[literal] = true
			}
			continue
		case "allow":
			before = "( !blergMutationGuard(" + strconv.Quote(id) + ") || ("
			after = "))"
		default:
			before = "(blergMutationGuard(" + strconv.Quote(id) + ") && ("
			after = "))"
		}
		edits[file] = append(edits[file], insertion{num(g, "start"), before}, insertion{num(g, "end"), after})
	}
	packages := map[string]bool{}
	for _, file := range mapKeys(edits) {
		raw, e := source(tree, file)
		if e != nil {
			return e
		}
		original, e := source(root, file)
		if e != nil {
			return e
		}
		if raw != original {
			return fmt.Errorf("commit selected source changes before mutation run: %s", file)
		}
		if e = os.WriteFile(filepath.Join(tree, file), []byte(insert(raw, edits[file])), 0600); e != nil {
			return e
		}
		if strings.HasSuffix(file, ".go") {
			packages[filepath.ToSlash(filepath.Dir(file))] = true
		}
	}
	for _, pkg := range mapKeys(packages) {
		files, _ := filepath.Glob(filepath.Join(tree, pkg, "*.go"))
		sort.Strings(files)
		name := ""
		for _, file := range files {
			if !strings.HasSuffix(file, "_test.go") {
				b, e := os.ReadFile(file) //nolint:gosec // Glob of Go sources inside the private archive package.
				if e != nil {
					return e
				}
				m := regexp.MustCompile(`(?m)^package (\w+)`).FindStringSubmatch(string(b))
				if len(m) > 1 {
					name = m[1]
					break
				}
			}
		}
		if name == "" {
			return fmt.Errorf("missing package declaration")
		}
		helper := "package " + name + "\nimport \"os\"\nfunc blergMutationGuard(id string) bool {return os.Getenv(\"BLERG_MUTATION_GUARD\") != id}\nvar blergMutationSQLSpans=map[string]map[string][2]int{"
		for _, literal := range mapKeys(spans[pkg]) {
			helper += strconv.Quote(literal) + ": {"
			for _, id := range mapKeys(spans[pkg][literal]) {
				s := spans[pkg][literal][id]
				helper += fmt.Sprintf("%q:{%d,%d},", id, s[0], s[1])
			}
			helper += "},"
		}
		helper += "}\nvar blergMutationSQLReplacements=map[string]string{"
		for _, id := range mapKeys(replacements) {
			helper += strconv.Quote(id) + ":" + strconv.Quote(replacements[id]) + ","
		}
		helper += "}\nfunc blergMutationSQL(literal,sql string) string {id:=os.Getenv(\"BLERG_MUTATION_GUARD\");span,ok:=blergMutationSQLSpans[literal][id];if !ok{return sql};if replacement,ok:=blergMutationSQLReplacements[id];ok{return sql[:span[0]]+replacement+sql[span[1]:]};return sql[:span[0]]+\"(\"+sql[span[0]:span[1]]+\" OR TRUE)\"+sql[span[1]:]}\n"
		if e := os.WriteFile(filepath.Join(tree, pkg, "blerg_mutation_probe.go"), []byte(helper), 0600); e != nil { //nolint:gosec // Package derives from fixed source inventory inside this private archive.
			return e
		}
	}
	return nil
}
func Backend(o Options) (int, error) {
	if err := validateDatabase(os.Getenv("DATABASE_URL"), os.Getenv("TEST_DATABASE_URL")); err != nil {
		return 2, err
	}
	env := envSet(os.Environ(), "GOWORK", "off")
	env = envSet(env, "PATH", filepath.Dir(o.Go)+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = envSet(env, "BLERG_MUTATION_GUARD", "")
	temp, e := os.MkdirTemp("", "blerg-live-mutations-")
	if e != nil {
		return 2, e
	}
	defer func() { _ = os.RemoveAll(temp) }()
	all, e := BackendCatalog(o.Root, o.Go, temp, env)
	if e != nil {
		return 2, e
	}
	if o.TestRun != "" {
		for _, g := range all {
			s := stringsOf(g["suite"])
			s[2] = o.TestRun
			g["suite"] = s
		}
	}
	guards, e := selectGuards(all, o.Filter, o.IDsFile)
	if e != nil {
		return 2, e
	}
	base, _, _, e := command(o.Root, nil, 0, "git", "rev-parse", "HEAD")
	if e != nil {
		return 2, e
	}
	files := map[string]bool{}
	for _, g := range all {
		files[str(g, "file")] = true
	}
	selection := o.Filter
	if selection == "" {
		selection = "all catalogued guards"
	}
	if o.IDsFile != "" {
		selection = "explicit guard IDs"
	}
	record := Object{"base_commit": strings.TrimSpace(base), "method": "isolated archive, one predicate disabled per fresh test process; no production flag changes", "catalog": mapKeys(files), "baselines": []Object{}, "guards": []Object{}, "selection": selection}
	if o.List {
		record["guards"] = guards
		e = save(o.Output, record)
		fmt.Println("Catalogued", len(guards), "guards")
		return 0, e
	}
	tree := filepath.Join(temp, "tree")
	if e = os.Mkdir(tree, 0700); e != nil {
		return 2, e
	}
	if e = archive(o.Root, tree); e != nil {
		return 2, e
	}
	if e = instrument(o.Root, tree, guards, all); e != nil {
		return 2, e
	}
	suites := map[string][]string{}
	for _, g := range guards {
		s := stringsOf(g["suite"])
		suites[strings.Join(s, "\x00")] = s
	}
	binaries := map[string]string{}
	deadlines := map[string]float64{}
	baselineRows := []Object{}
	fail := func(message string) (int, error) { record["error"] = message; return 2, save(o.Output, record) }
	for _, key := range mapKeys(suites) {
		s := suites[key]
		pkgKey := s[0] + "/" + s[1]
		binary := binaries[pkgKey]
		if binary == "" {
			binary = filepath.Join(temp, strings.ReplaceAll(pkgKey, "/", "-")+".test")
			_, exit, _, err := command(filepath.Join(tree, s[0]), env, 0, o.Go, "test", "-c", "-o", binary, "./"+s[1])
			if err != nil {
				return 2, err
			}
			if _, err = os.Stat(binary); exit != 0 || err != nil {
				return fail("instrumented compile failed: " + pkgKey)
			}
			binaries[pkgKey] = binary
		}
		started := time.Now()
		output, exit, _, err := command(filepath.Join(tree, s[0]), env, 0, binary, "-test.run="+s[2], "-test.timeout=60s", "-test.v")
		if err != nil {
			return 2, err
		}
		seconds := rounded(time.Since(started))
		deadline := max(10., min(60., seconds*3+5))
		deadlines[key] = deadline
		baselineRows = append(baselineRows, Object{"seconds": seconds, "mutant_deadline_seconds": deadline, "suite": s, "exit_code": exit, "failed_tests": matches(`--- FAIL: ([^\s(]+)`, output), "skipped_tests": matches(`--- SKIP: ([^\s(]+)`, output), "passed_tests": matches(`--- PASS: ([^\s(]+)`, output)})
		record["baselines"] = baselineRows
		if exit != 0 {
			return fail("instrumented baseline failed")
		}
		fmt.Println("BASELINE", pkgKey, s[2], "PASS")
	}
	observed := []Object{}
	for i, g := range guards {
		s := stringsOf(g["suite"])
		key := strings.Join(s, "\x00")
		deadline := deadlines[key]
		runEnv := envSet(env, "BLERG_MUTATION_GUARD", str(g, "id"))
		runEnv = envSet(runEnv, "PGOPTIONS", strings.TrimSpace(os.Getenv("PGOPTIONS")+" -c blerg.mutation_guard="+str(g, "id")))
		started := time.Now()
		output, exit, timed, err := command(filepath.Join(tree, s[0]), runEnv, time.Duration((deadline+5)*float64(time.Second)), binaries[s[0]+"/"+s[1]], "-test.run="+s[2], fmt.Sprintf("-test.timeout=%.3fs", deadline), "-test.v")
		if err != nil {
			return 2, err
		}
		row := clone(g)
		row["seconds"] = rounded(time.Since(started))
		row["deadline_seconds"] = deadline
		if timed {
			row["status"] = "timeout"
		} else {
			row["status"] = goOutcome(exit, output)
			row["exit_code"] = exit
			row["failed_tests"] = unique(matches(`--- FAIL: ([^\s(]+)`, output))
		}
		observed = append(observed, row)
		record["guards"] = observed
		if e = save(o.Output, record); e != nil {
			return 2, e
		}
		fmt.Println(i+1, "/", len(guards), g["id"], row["status"])
	}
	record["summary"] = summary(observed)
	if e = save(o.Output, record); e != nil {
		return 2, e
	}
	fmt.Println(record["summary"])
	for _, g := range observed {
		if str(g, "status") != "killed" {
			return 1, nil
		}
	}
	return 0, nil
}
