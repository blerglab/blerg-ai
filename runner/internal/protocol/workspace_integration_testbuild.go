//go:build blerg_workspace_integration

package protocol

// Test-only build: never use this tag for shipped binaries. This one switch
// exercises both transport gates without changing either release constant.
const workspaceIntegrationBuild = true
