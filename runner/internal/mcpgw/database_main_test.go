package mcpgw

import (
	"fmt"
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func TestMain(m *testing.M) {
	code := testsupport.Run(m)
	if err := realBoardBinary.cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "remove real Board test executable: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
