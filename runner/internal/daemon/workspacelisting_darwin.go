//go:build darwin

package daemon

func workspaceOpenListing(e *LocalExplorer, name string) (*workspaceListing, error) {
	return workspaceRetainedListing(e, name)
}
