package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/shellpolicy"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The ordinary verifier must never opt into standing authority implicitly.
func TestStandingWireScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proof coreProof
		want  bool
	}{
		{"standing child owner", coreProof{SessionID: "old-login", standing: true}, true},
		{"limit resume", coreProof{SessionID: "old-login"}, false},
		{"managed credential", coreProof{SessionID: "old-login"}, false},
		{"shell", coreProof{SessionID: "old-login"}, false},
		{"token origin", coreProof{TokenID: "agent-token", standing: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				got, _ := body["standing"].(bool)
				if got != tc.want {
					t.Errorf("standing=%v, want %v", got, tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"engines":[]}`))
			}))
			defer srv.Close()
			if err := verifyCorePrincipal(context.Background(), srv.Client(), srv.URL, "fixture-key", "owner", tc.proof); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Pin every production constructor/assignment that can carry standing authority.
// A new site requires an explicit policy review and this exact inventory change.
func TestStandingCallSiteAllowlist(t *testing.T) {
	root := ".."
	want := map[string]int{
		"mcpgw/board.go:exchange":                                             1,
		"mcpgw/core.go:Token":                                                 1,
		"server/coreprincipal.go:listCorePersonalKinds":                       1,
		"server/k8sjobs.go:fetchCoreCredential":                               1,
		"server/k8sjobs.go:proof":                                             1,
		"server/mcpstart.go:listConnections":                                  1,
		"server/standing_permission_resources.go:permissionResourceAuthority": 1,
		"server/sessionboards.go:listSessionBoards":                           1,
		"server/delegation_cluster.go:clusterOwnerState":                      1,
		"server/delegation_cluster.go:startClusterChild":                      2,
		"server/session_permissions.go:permissionAuthority":                   1,
	}
	wantExchanges := map[string][]string{
		"mcpgw/board.go:Exchange":              {"standing"},
		"mcpgw/board.go:ExchangeReadOnly":      {"standing"},
		"mcpgw/board.go:boardCredential":       {"true"},
		"mcpgw/boardlevel.go:CallBoard":        {"!c.CutExecutor", "!c.CutExecutor"},
		"server/board_filing_http.go:read":     {"true"},
		"server/board_filing_http.go:Describe": {"true"},
		"server/cronfailurecard.go:PostCard":   {"false"},
	}
	gotExchanges := map[string][]string{}
	got := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		isStanding := func(name string) bool {
			return name == "standing" || name == "Standing" || name == "standingChildStart"
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				hit := false
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Exchange" || sel.Sel.Name == "ExchangeReadOnly" || sel.Sel.Name == "exchange" && strings.HasPrefix(filepath.ToSlash(rel), "mcpgw/")) {
						last := 4
						if sel.Sel.Name == "exchange" {
							last = 5
						}
						if len(x.Args) != last+1 {
							t.Fatalf("exchange omitted explicit authority: %s:%s", rel, fn.Name.Name)
						}
						var flag strings.Builder
						if err := format.Node(&flag, token.NewFileSet(), x.Args[last]); err != nil {
							t.Fatal(err)
						}
						site := filepath.ToSlash(rel) + ":" + fn.Name.Name
						gotExchanges[site] = append(gotExchanges[site], flag.String())
					}
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "listConnections" && len(x.Args) == 3 {
						if flag, ok := x.Args[2].(*ast.Ident); ok && flag.Name == "true" {
							hit = true
						}
					}

				case *ast.KeyValueExpr:
					if key, ok := x.Key.(*ast.Ident); ok && isStanding(key.Name) {
						hit = true
					}
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && isStanding(sel.Sel.Name) {
							hit = true
						}
					}
				}
				if hit {
					got[filepath.ToSlash(rel)+":"+fn.Name.Name]++
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotExchanges, wantExchanges) {
		t.Fatalf("actual exchange authority callers changed: got %v want %v", gotExchanges, wantExchanges)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("standing call sites changed:\n got %v\nwant %v", got, want)
	}
	var boardCall mcpgw.BoardCall
	if err := json.Unmarshal([]byte(`{"Standing":true,"CutExecutor":true}`), &boardCall); err != nil {
		t.Fatal(err)
	}
	if boardCall.CutExecutor {
		t.Fatal("request JSON selected server-only board execution authority")
	}
	var p coreProof
	if err := json.Unmarshal([]byte(`{"SessionID":"login","standing":true,"Standing":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.standing {
		t.Fatal("request JSON selected server-only standing authority")
	}
}

func TestStandingExcludedCredentialAndShellPaths(t *testing.T) {
	paths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path]++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["standing"] == true {
			t.Errorf("excluded path %s sent standing authority", r.URL.Path)
		}
		http.Error(w, `{"code":"not_managed"}`, http.StatusConflict)
	}))
	defer srv.Close()
	_, _, _ = fetchManagedAccess(t.Context(), srv.Client(), srv.URL, "fixture-key", managedAccessRequest{AccountID: "owner", Engine: "codex", SessionID: "old-login"})
	shell := shellAuthority{url: srv.URL, key: "fixture-key", client: srv.Client()}
	_, _, _ = shell.authorizationCall(t.Context(), shellpolicy.AuthorizationRequest{Owner: "owner", Login: "old-login"})
	if paths["/internal/credentials/managed/access"] != 1 || paths["/internal/shell/authorize"] != 1 {
		t.Fatalf("paths=%v", paths)
	}
	if (SessionJobSpec{AuthSessionID: "old-login"}).proof().standing {
		t.Fatal("ordinary/recovery job acquired standing authority")
	}
	if !(SessionJobSpec{AuthSessionID: "old-login", standingChildStart: true}).proof().standing {
		t.Fatal("child start lost standing authority")
	}
}
