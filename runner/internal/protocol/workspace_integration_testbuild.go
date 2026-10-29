//go:build blerg_workspace_integration

package protocol

// Test-only acceptance harness compatibility. Released cluster support is
// enabled in ordinary builds; this tag never enables macOS host support.
const workspaceIntegrationBuild = true
