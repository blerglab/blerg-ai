//go:build !linux && !darwin

package discovery

import "github.com/blerglab/blerg-ai/runner/internal/protocol"

func (s *Service) initialize(_ *rootHandle, _ protocol.DiscoverWorkspace, _ *operation) (selectionRecord, error) {
	return selectionRecord{}, errUnavailable
}
