package shellruntime

import "testing"

func TestServicePlatformUsesHelperExecutionRuntime(t *testing.T) {
	for _, tc := range []struct {
		runtime, platform string
		want              bool
	}{
		{"host", "linux", true}, {"sandbox", "linux", true}, {"cluster", "linux", true},
		{"sandbox", "darwin", true}, {"host", "darwin", false}, {"cluster", "darwin", false},
		{"sandbox", "windows", false}, {"unknown", "linux", false},
	} {
		if got := servicePlatformSupported(tc.runtime, tc.platform); got != tc.want {
			t.Fatalf("%s/%s support=%v, want %v", tc.runtime, tc.platform, got, tc.want)
		}
	}
}
