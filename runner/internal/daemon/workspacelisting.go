package daemon

import (
	"os"
)

type workspaceListing struct {
	file      *os.File
	openChild func(string) (*os.File, error)
	close     func() error
	validate  func() bool
}
