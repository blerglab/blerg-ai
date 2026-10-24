//go:build !darwin

package daemon

import (
	"os"
	"path"
)

func workspaceOpenListing(e *LocalExplorer, name string) (*workspaceListing, error) {
	dir, err := e.openDirectory(name)
	if err != nil {
		return nil, err
	}
	return &workspaceListing{file: dir, openChild: func(child string) (*os.File, error) {
		return e.openFile(path.Join(name, child))
	}, close: dir.Close}, nil
}
