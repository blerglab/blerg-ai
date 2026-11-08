package main

import "os"

// Builds retain explicit cache/worker controls, without inheriting credentials.
func goBuildEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOWORK=off"}
	for _, key := range []string{"GOCACHE", "GOTMPDIR", "GOFLAGS", "GOMAXPROCS", "GOTOOLCHAIN", "GOMODCACHE"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
