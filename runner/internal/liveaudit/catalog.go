package liveaudit

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var catalogPatterns = []string{"contracts/livedata/*.go", "contracts/livetls/*.go", "contracts/identity/*.go", "contracts/livemcp/*.go", "core/internal/livedata/*.go", "core/internal/api/live_data*.go", "core/cmd/blerg-core/live_data*.go", "core/internal/identity/live_data_approval.go", "core/internal/identity/human_session.go", "core/internal/identity/agent_tokens.go", "core/internal/api/internal_handlers.go", "core/internal/mcpconn/service.go", "runner/internal/livepoll/*.go", "runner/internal/mcpgw/live*.go", "runner/internal/server/live_data*.go", "runner/internal/server/datasets_mcp*.go", "runner/internal/server/datasets.go", "runner/internal/server/bound_asks.go", "runner/internal/server/bound_live_asks.go"}

func suiteFor(file string) []string {
	parts := strings.SplitN(file, "/", 2)
	module := parts[0]
	pkg := filepath.ToSlash(filepath.Dir(parts[1]))
	name := filepath.Base(file)
	regex := "^Test"
	switch file {
	case "core/internal/identity/agent_tokens.go":
		return []string{"core", "internal/identity", "^TestAgentTokenLive$"}
	case "core/internal/identity/human_session.go":
		return []string{"core", "internal/api", "^TestLiveData"}
	case "core/internal/mcpconn/service.go":
		return []string{"core", "internal/mcpconn", "^Test(FetchSecret|ScopedToAccount|Encryption)"}
	case "core/internal/identity/live_data_approval.go":
		return []string{"core", "internal/api", "^TestLiveDataHumanTransactionRechecksAuthority$"}
	}
	switch {
	case module == "contracts":
	case module == "core" && pkg == "internal/identity":
		regex = "^TestRevokeAccountEverywhereAdvancesLiveDataEpoch$"
	case module == "core" && pkg == "internal/livedata":
		switch name {
		case "issuer.go", "keys.go":
			regex = "^Test(Issuer|KeyRotation|PrivateKey)"
		case "transport.go":
			regex = "^TestIssuer.*Transport"
		case "postgres.go", "retention.go":
			regex = "^Test(PostgresDurableAuthority|LiveAuditRetention)"
		case "revocation_checkpoint.go":
			regex = "^TestRevocationCheckpoint"
		default:
			regex = "^Test(External|SecretAuthority|RevocationCheckpoint)"
		}
	case module == "core":
		regex = map[string]string{"live_data_posture.go": "^TestLiveDataPosture", "live_data_tls.go": "^TestLiveDataTLS", "live_data_renew_job.go": "^TestLiveDataRenewJob", "live_data_fence.go": "^TestLiveDataExplicitFence"}[name]
		if regex == "" {
			regex = "^TestLiveData"
		}
	case pkg == "internal/livepoll":
		switch name {
		case "verifier.go":
			regex = "^TestVerifier"
		case "attempts.go":
			regex = "^TestAttempt"
		case "scheduler.go", "failure.go":
			regex = "^TestScheduler|^TestVerifier"
		default:
			regex = "^TestSchedule|^TestAttemptQueue"
		}
	case pkg == "internal/mcpgw":
		regex = "^TestLive"
	default:
		regex = "^Test(DatasetMCP|MCPAuthority|LivePosture|LiveApproval|LiveAttempt|LiveDefinition|LiveOutbox|BoundLiveAsk)"
	}
	return []string{module, pkg, regex}
}

type sqlSpan struct {
	a, b  int
	value string
}

func sqlPredicates(text string) []sqlSpan {
	type token struct {
		word        string
		a, b, depth int
	}
	re := regexp.MustCompile(`'(?:''|[^'])*'|"(?:""|[^"])*"|--[^\n]*|[A-Za-z_][A-Za-z_0-9]*|[()]`)
	tokens := []token{}
	depth := 0
	for _, p := range re.FindAllStringIndex(text, -1) {
		word := text[p[0]:p[1]]
		if strings.HasPrefix(word, "'") || strings.HasPrefix(word, "\"") || strings.HasPrefix(word, "--") {
			continue
		}
		if word == ")" {
			depth--
		}
		tokens = append(tokens, token{strings.ToUpper(word), p[0], p[1], depth})
		if word == "(" {
			depth++
		}
	}
	ends := map[string]bool{}
	for _, s := range strings.Fields("AND OR ORDER GROUP HAVING LIMIT FOR RETURNING UNION SET FROM JOIN LEFT RIGHT INNER ON WHEN THEN ELSE END") {
		ends[s] = true
	}
	found := []sqlSpan{}
	for i, t := range tokens {
		if t.word != "WHERE" && t.word != "AND" && t.word != "OR" && t.word != "ON" {
			continue
		}
		if t.word == "ON" && i+1 < len(tokens) && tokens[i+1].word == "CONFLICT" {
			continue
		}
		stop := len(text)
		for _, n := range tokens[i+1:] {
			if n.depth < t.depth || n.depth == t.depth && ends[n.word] {
				stop = n.a
				break
			}
		}
		a, b := t.b, stop
		for a < b && strings.ContainsRune(" \t\r\n\v\f", rune(text[a])) {
			a++
		}
		for b > a && strings.ContainsRune(" \t\r\n\v\f", rune(text[b-1])) {
			b--
		}
		v := text[a:b]
		bare := regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)?$`).MatchString(v)
		switch strings.ToUpper(v) {
		case "TRUE", "FALSE", "NULL", "DEFAULT", "CASE", "CONSTRAINT":
			bare = false
		}
		if v != "" && (strings.ContainsAny(v, "=<>") || regexp.MustCompile(`(?i)\b(IS|EXISTS|NOT)\b`).MatchString(v) || bare) {
			found = append(found, sqlSpan{a, b, v})
		}
	}
	return found
}
func guard(file, fn, raw string, a, b int, kind string) Object {
	return Object{"file": file, "function": fn, "start": a, "end": b, "line": strings.Count(raw[:a], "\n") + 1, "expression": raw[a:b], "kind": kind}
}
func BackendCatalog(root, goBin, temp string, env []string) ([]Object, error) {
	set := map[string]bool{}
	for _, pattern := range catalogPatterns {
		paths, e := filepath.Glob(filepath.Join(root, pattern))
		if e != nil {
			return nil, e
		}
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			r, _ := filepath.Rel(root, p)
			set[filepath.ToSlash(r)] = true
		}
	}
	files := mapKeys(set)
	inventory := filepath.Join(temp, "inventory")
	_, code, _, e := command(root, env, 0, goBin, "build", "-o", inventory, filepath.Join(root, "scripts/live_data_guard_inventory.go"))
	if e != nil {
		return nil, fmt.Errorf("inventory compile failed: %w", e)
	}
	if code != 0 {
		return nil, fmt.Errorf("inventory compile failed (exit %d)", code)
	}
	out, code, _, e := command(root, env, 0, inventory, files...)
	if e != nil || code != 0 {
		return nil, fmt.Errorf("inventory failed")
	}
	var guards []Object
	if e = json.Unmarshal([]byte(out), &guards); e != nil {
		return nil, e
	}
	literals := regexp.MustCompile("`([^`]*)`")
	scoped := map[string][]string{"core/internal/api/internal_handlers.go": {"principalLiveness", "validInternalKey"}, "core/internal/identity/human_session.go": {"accountState", "HumanSessionDeadline"}, "core/internal/identity/agent_tokens.go": {"AgentTokenLive"}, "core/internal/mcpconn/service.go": {"FetchSecret"}}
	decl := regexp.MustCompile(`(?m)^func (?:\([^\n]*?\) )?(\w+)\(`)
	for _, file := range files {
		raw, e := source(root, file)
		if e != nil {
			return nil, e
		}
		allowed := [][2]int{}
		declarations := decl.FindAllStringSubmatchIndex(raw, -1)
		for i, d := range declarations {
			for _, name := range scoped[file] {
				if raw[d[2]:d[3]] == name {
					end := len(raw)
					if i+1 < len(declarations) {
						end = declarations[i+1][0]
					}
					allowed = append(allowed, [2]int{d[0], end})
				}
			}
		}
		for _, lit := range literals.FindAllStringSubmatchIndex(raw, -1) {
			if _, ok := scoped[file]; ok {
				valid := false
				for _, span := range allowed {
					valid = valid || span[0] <= lit[0] && lit[0] < span[1]
				}
				if !valid {
					continue
				}
			}
			text := raw[lit[2]:lit[3]]
			if !regexp.MustCompile(`\b(SELECT|UPDATE|DELETE|INSERT|WHERE|AND)\b`).MatchString(text) {
				continue
			}
			for _, s := range sqlPredicates(text) {
				g := guard(file, "SQL predicate", raw, lit[2]+s.a, lit[2]+s.b, "sql")
				g["literal_start"] = lit[0]
				g["literal_end"] = lit[1]
				g["relative_start"] = s.a
				g["relative_end"] = s.b
				guards = append(guards, g)
			}
		}
	}
	file := "runner/internal/livepoll/schedule_store.go"
	raw, e := source(root, file)
	if e != nil {
		return nil, e
	}
	for _, lit := range literals.FindAllStringSubmatchIndex(raw, -1) {
		if !strings.Contains(raw[lit[2]:lit[3]], "WITH queued AS") {
			continue
		}
		for _, s := range [][3]string{{"80", "81", "^TestScheduleQueueWindowIsEighty$"}, {"owner_turn,next_due_at,binding_id", "next_due_at,binding_id", "^TestScheduleFairOwnerRotationBeyondQueueWindow$"}} {
			a := strings.Index(raw[lit[0]:], s[0]) + lit[0]
			if s[0] == "80" {
				a = strings.Index(raw[lit[0]:], "LIMIT 80") + lit[0] + len("LIMIT ")
			}
			g := guard(file, "SQL scheduling boundary", raw, a, a+len(s[0]), "sql")
			g["replacement"] = s[1]
			g["proof_test"] = s[2]
			g["literal_start"] = lit[0]
			g["literal_end"] = lit[1]
			g["relative_start"] = a - lit[2]
			g["relative_end"] = a + len(s[0]) - lit[2]
			guards = append(guards, g)
		}
		break
	}
	for _, file = range []string{"contracts/livedata/transport.go", "core/internal/livedata/secret_state.go", "core/cmd/blerg-core/live_data_posture.go", "core/cmd/blerg-core/live_data_renew_job.go", "core/cmd/blerg-core/live_data_provider.go"} {
		raw, e = source(root, file)
		if e != nil {
			return nil, e
		}
		for _, p := range regexp.MustCompile(`(?m)^[ \t]*client\.CheckRedirect = [^\n]+`).FindAllStringIndex(raw, -1) {
			g := guard(file, "redirect refusal assignment", raw, p[0], p[1], "statement")
			g["expression"] = strings.TrimSpace(raw[p[0]:p[1]])
			guards = append(guards, g)
		}
	}
	type definition struct {
		file, raw, name string
		a, b            int
	}
	functions := map[string]definition{}
	defRE := regexp.MustCompile(`(?is)CREATE OR REPLACE FUNCTION (live_[a-z_]+)\(.*?AS \$\$(.*?)\$\$;`)
	for _, module := range []string{"core", "runner"} {
		paths, _ := filepath.Glob(filepath.Join(root, module, "internal/db/migrations/*live*sql"))
		sort.Strings(paths)
		for _, p := range paths {
			file, _ := filepath.Rel(root, p)
			raw, e := source(root, file)
			if e != nil {
				return nil, e
			}
			for _, d := range defRE.FindAllStringSubmatchIndex(raw, -1) {
				name := raw[d[2]:d[3]]
				functions[module+":"+name] = definition{filepath.ToSlash(file), raw, name, d[4], d[5]}
			}
		}
	}
	for _, key := range mapKeys(functions) {
		d := functions[key]
		for _, p := range regexp.MustCompile(`(?is)\bIF\s+(.*?)\s+THEN\b`).FindAllStringSubmatchIndex(d.raw[d.a:d.b], -1) {
			guards = append(guards, guard(d.file, d.name, d.raw, d.a+p[2], d.a+p[3], "trigger"))
		}
	}
	file = "core/internal/identity/human_session.go"
	raw, e = source(root, file)
	if e != nil {
		return nil, e
	}
	a := strings.Index(raw, "live_data_epoch+1") + len("live_data_epoch+")
	guards = append(guards, guard(file, "RevokeAccountEverywhereTx epoch advance", raw, a, a+1, "effect"))
	sort.SliceStable(guards, func(i, j int) bool {
		a, b := guards[i], guards[j]
		if a["file"] != b["file"] {
			return str(a, "file") < str(b, "file")
		}
		if num(a, "start") != num(b, "start") {
			return num(a, "start") < num(b, "start")
		}
		return num(a, "end") < num(b, "end")
	})
	for _, g := range guards {
		raw, e := source(root, str(g, "file"))
		if e != nil {
			return nil, e
		}
		g["id"] = "live-" + digest([]byte(str(g, "file") + ":" + str(g, "function") + ":" + strconv.Itoa(num(g, "start")) + ":" + str(g, "expression")))[:12]
		g["source_sha256"] = digest([]byte(raw))
		suite := suiteFor(str(g, "file"))
		switch str(g, "kind") {
		case "effect":
			suite = []string{"core", "internal/identity", "^TestRevokeAccountEverywhereAdvancesLiveDataEpoch$"}
		case "trigger":
			if strings.HasPrefix(str(g, "file"), "core/") {
				suite = []string{"core", "internal/livedata", "^Test(PostgresDurableAuthority|RevocationCheckpoint|LiveAuditRetention)"}
			} else {
				suite = []string{"runner", "internal/server", "^Test(LiveOutbox|BoundLiveAsk|DatasetMCP)"}
			}
		}
		if str(g, "proof_test") != "" {
			suite[2] = str(g, "proof_test")
		}
		g["suite"] = suite
	}
	return guards, nil
}

// Ensure the standard library is the only tool dependency.
