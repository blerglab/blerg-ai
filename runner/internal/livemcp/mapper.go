// Package livemcp retains the Runner mapper API while Core approval and Runner
// execution use the same bounded declarative mapping contract.
package livemcp

import shared "github.com/blerglab/blerg-ai/contracts/livemcp"

const MaxRows = shared.MaxRows
const MaxBytes = shared.MaxBytes

type Field = shared.Field
type Mapping = shared.Mapping
type Row = shared.Row

func Validate(m Mapping) error                    { return shared.Validate(m) }
func Map(m Mapping, sample []byte) ([]Row, error) { return shared.Map(m, sample) }
