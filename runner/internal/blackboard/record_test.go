package blackboard_test

import (
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard/storetest"
	"testing"
)

func TestMemoryStoreContract(t *testing.T) { storetest.StoreContract(t, blackboard.NewMemoryStore()) }
