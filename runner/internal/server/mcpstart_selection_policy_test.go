package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestGrantSelectedToolsPolicy(t *testing.T) {
	allow := map[string]mcpgw.ToolGrant{"echo": {Mode: mcpgw.ModeAllow, Hash: "h"}}
	for _, tc := range []struct {
		name       string
		tools      map[string]MCPToolSelection
		noDefaults bool
		status     int
		message    string
		want       map[string]mcpgw.ToolGrant
	}{
		{name: "tool without a hash", tools: map[string]MCPToolSelection{"echo": {Mode: "allow"}}, status: 400, message: "hash you saw"},
		{name: "mode off is not selectable", tools: map[string]MCPToolSelection{"echo": {Mode: "off", Hash: "h"}}, status: 400, message: "mode must be"},
		{name: "unknown mode", tools: map[string]MCPToolSelection{"echo": {Mode: "yolo", Hash: "h"}}, status: 400, message: "mode must be"},
		{name: "a present but empty tools object", tools: map[string]MCPToolSelection{}, status: 400, message: "selects no tools"},
		{name: "no tools and no defaults", noDefaults: true, status: 422, message: "no default tools"},
		{name: "allow", tools: map[string]MCPToolSelection{"echo": {Mode: "allow", Hash: "h"}}, want: allow},
		{name: "propose", tools: map[string]MCPToolSelection{"echo": {Mode: "propose", Hash: "h"}}, want: map[string]mcpgw.ToolGrant{"echo": {Mode: mcpgw.ModePropose, Hash: "h"}}},
		{name: "defaults", want: allow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection := MCPConnection{Name: mcpConn, DefaultTools: allow}
			if tc.noDefaults {
				connection.DefaultTools = nil
			}
			tools, problem := selectedTools(connection, MCPSelection{Tools: tc.tools})
			if tc.status == 0 {
				if problem != nil || !reflect.DeepEqual(tools, tc.want) {
					t.Fatalf("tools = %#v, problem = %v; want %#v", tools, problem, tc.want)
				}
			} else if tools != nil || problem == nil || problem.Status != tc.status || !strings.Contains(problem.Message, tc.message) {
				t.Fatalf("tools = %#v, problem = %v; want nil, %d containing %q", tools, problem, tc.status, tc.message)
			}
		})
	}
}
