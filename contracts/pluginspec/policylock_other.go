//go:build !unix

package pluginspec

import "errors"

func lockPolicy(string) (func(), error) {
	return nil, errors.New("persistent marketplace policy requires Unix file locking")
}
