package mcpconn_test

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Fingerprints make an exception apply to one reviewed SQL expression, not to
// an entire file or method. New/changed queries must declare their scope.
var unscopedMCPQueries = map[string]string{
	"core/internal/mcpconn/oauth.go:d85e4c7da5d6c5d1":   "Shared per-account quota intentionally counts account and live session credentials.",
	"core/internal/mcpconn/service.go:8a3c0b7cd3f2ab1d": "Shared per-account quota intentionally counts account and live session credentials.",
	"core/internal/mcpconn/session.go:d85e4c7da5d6c5d1": "Shared per-account quota intentionally counts account and live session credentials.",
	"core/internal/mcpconn/service.go:f9329d018619892a": "Account-only insertion omits every scope column; the database all-or-none CHECK requires all to remain NULL.",
	"core/internal/mcpconn/service.go:19af187d695de6b5": "Aggregate URL diagnostic intentionally counts both scopes and returns no connection rows or secrets.",
}

func mcpSQLExpressions(source string) ([]string, error) {
	tree, err := parser.ParseFile(token.NewFileSet(), "source.go", source, 0)
	if err != nil {
		return nil, err
	}
	var queries []string
	ast.Inspect(tree, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BinaryExpr, *ast.BasicLit:
		default:
			return true
		}
		var parts []string
		ast.Inspect(n, func(child ast.Node) bool {
			switch v := child.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					s, e := strconv.Unquote(v.Value)
					if e == nil {
						parts = append(parts, s)
					}
				}
			case *ast.Ident:
				parts = append(parts, v.Name)
			}
			return true
		})
		text := strings.Join(parts, " ")
		if strings.Contains(strings.ToLower(text), "mcp_connections") && len(strings.Fields(text)) > 1 {
			queries = append(queries, text)
		}
		return false // maximal concatenation only; do not double-count its literals
	})
	return queries, nil
}
func scopedMCPQuery(q string) bool {
	return strings.Contains(q, "runner_session_id") || strings.Contains(q, "sessionWhere")
}
func mcpQueryKey(path, q string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(q), " ")))
	return fmt.Sprintf("%s:%x", path, sum[:8])
}

func TestMCPConnectionSQLScopeGuard(t *testing.T) {
	root := filepath.Clean("../../..")
	seen := map[string]bool{}
	for _, module := range []string{"core", "runner", "board"} {
		err := filepath.WalkDir(filepath.Join(root, module), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == "vendor" || entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			queries, e := mcpSQLExpressions(string(data))
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(root, path)
			for _, q := range queries {
				if scopedMCPQuery(q) {
					continue
				}
				key := mcpQueryKey(filepath.ToSlash(rel), q)
				if reason := unscopedMCPQueries[key]; reason != "" {
					seen[key] = true
					continue
				}
				t.Errorf("SQL needs explicit scope or reviewed exception: %s\n%s", key, q)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range unscopedMCPQueries {
		if !seen[key] {
			t.Errorf("stale SQL exception: %s", key)
		}
	}
}

func TestMCPConnectionSQLScopeGuardDetectsNewQuery(t *testing.T) {
	queries, err := mcpSQLExpressions("package fixture; func f(){ q:=`SELECT * FROM mcp_connections WHERE account_id=$1`; _=q }")
	if err != nil || len(queries) != 1 || scopedMCPQuery(queries[0]) {
		t.Fatal("new unscoped query not detected")
	}
	queries, err = mcpSQLExpressions("package fixture; func f(){ q:=`SELECT * FROM mcp_connections WHERE `+sessionWhere; _=q }")
	if err != nil || len(queries) != 1 || !scopedMCPQuery(queries[0]) {
		t.Fatal("scoped concatenation not recognized")
	}
}
