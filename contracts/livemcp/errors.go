package livemcp

import "errors"

var (
	ErrInvalidMapping = errors.New("invalid_mapping")
	ErrInvalidSample  = errors.New("invalid_sample")
	ErrSampleLimit    = errors.New("sample_limit")
)
