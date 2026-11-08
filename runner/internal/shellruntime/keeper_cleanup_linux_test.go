//go:build linux

package shellruntime

import "testing"

func TestColdKeeperRefusesResidualUnitTasks(t *testing.T) {
	for _, data := range []string{"", "bad", "0", "41\n42\n", "42\n"} {
		if err := keeperGroupEmptyExceptSelf(data, 41); err == nil {
			t.Fatal("residual or unproven unit membership was empty custody", data)
		}
	}
	if err := keeperGroupEmptyExceptSelf("41\n", 41); err != nil {
		t.Fatal("clean unit rejected", err)
	}
}
