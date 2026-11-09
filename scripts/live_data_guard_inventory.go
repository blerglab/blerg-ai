//go:build ignore

// Emits rejection guards from the explicitly selected live-data source files.
// It never edits the production checkout. Used by the Go live-data-audit backend command.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
)

type Guard struct {
	File       string `json:"file"`
	Function   string `json:"function"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Line       int    `json:"line"`
	Expression string `json:"expression"`
	Kind       string `json:"kind"`
}

func main() {
	var guards []any
	denied := regexp.MustCompile(`refuse\(|boundFailure\(|boundConversation\(|\bfail\(|Err[A-Z][A-Za-z]+|liveFail\(|writeError\(|http.Error\(|return false|return[^;\n]*,\s*(?:[a-zA-Z_]*[Ee]rr|e)\b|return 0`)
	for _, path := range os.Args[1:] {
		raw, e := os.ReadFile(path)
		if e != nil {
			panic(e)
		}
		fs := token.NewFileSet()
		file, e := parser.ParseFile(fs, path, raw, 0)
		if e != nil {
			panic(e)
		}
		cut := func(n ast.Node) string { return string(raw[fs.Position(n.Pos()).Offset:fs.Position(n.End()).Offset]) }
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			selected := map[string]string{
				"core/internal/api/internal_handlers.go":  " principalLiveness validInternalKey ",
				"core/internal/identity/human_session.go": " accountState HumanSessionDeadline ",
				"core/internal/identity/agent_tokens.go":  " AgentTokenLive ",
				"core/internal/mcpconn/service.go":        " FetchSecret ",
			}
			if names, scoped := selected[path]; scoped && !strings.Contains(names, " "+fn.Name.Name+" ") {
				continue
			}
			if strings.HasSuffix(path, "/datasets.go") && fn.Name.Name != "filterDatasetDelivery" && fn.Name.Name != "datasetVisibilityCause" {
				continue
			}
			if strings.HasSuffix(path, "/bound_asks.go") && fn.Name.Name != "createBoundAsk" && fn.Name.Name != "boundLoginLive" {
				continue
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if choice, ok := n.(*ast.SwitchStmt); ok {
					tag, tagStart, tagEnd, safe := "", 0, 0, choice.Tag == nil
					if choice.Tag != nil {
						switch choice.Tag.(type) {
						case *ast.Ident, *ast.SelectorExpr:
							safe = true
						}
						if safe {
							tag = cut(choice.Tag)
							tagStart = fs.Position(choice.Tag.Pos()).Offset
							tagEnd = fs.Position(choice.Tag.End()).Offset
						}
					}
					if safe {
						for _, item := range choice.Body.List {
							clause := item.(*ast.CaseClause)
							for _, value := range clause.List {
								guards = append(guards, map[string]any{"file": path, "function": fn.Name.Name, "start": fs.Position(value.Pos()).Offset, "end": fs.Position(value.End()).Offset, "line": fs.Position(value.Pos()).Line, "expression": cut(value), "kind": "case", "tag": tag, "tag_start": tagStart, "tag_end": tagEnd})
							}
						}
					}
				}

				branch, ok := n.(*ast.IfStmt)
				if !ok {
					return true
				}
				var direct strings.Builder
				for _, stmt := range branch.Body.List {
					switch stmt.(type) {
					case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
						continue
					}
					direct.WriteString(cut(stmt))
					direct.WriteByte('\n')
				}
				kind := "branch"
				if denied.MatchString(direct.String()) {
					kind = "reject"
				}
				guards = append(guards, Guard{path, fn.Name.Name, fs.Position(branch.Cond.Pos()).Offset, fs.Position(branch.Cond.End()).Offset, fs.Position(branch.Cond.Pos()).Line, cut(branch.Cond), kind})
				return true
			})
			if booleanResult(fn) {
				for _, stmt := range fn.Body.List {
					ret, ok := stmt.(*ast.ReturnStmt)
					if !ok || len(ret.Results) != 1 {
						continue
					}
					expr := ret.Results[0]
					if id, ok := expr.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
						continue
					}
					guards = append(guards, Guard{path, fn.Name.Name, fs.Position(expr.Pos()).Offset, fs.Position(expr.End()).Offset, fs.Position(expr.Pos()).Line, cut(expr), "allow"})
					guards = append(guards, Guard{path, fn.Name.Name + " false result", fs.Position(expr.Pos()).Offset, fs.Position(expr.End()).Offset, fs.Position(expr.Pos()).Line, cut(expr), "false_result"})
				}
			}
		}
	}
	if json.NewEncoder(os.Stdout).Encode(guards) != nil {
		os.Exit(1)
	}
}

func booleanResult(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	id, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "bool"
}
