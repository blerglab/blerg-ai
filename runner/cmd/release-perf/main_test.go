package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// npmBuild must send a frontend's build output to the caller's own outDir,
// outside the source checkout, and must never write anything into the
// source directory itself (MED b, Codex review of d7e50390: tsc -b's
// "build" npm script wrote dist/ and tsconfig.tsbuildinfo straight into
// the checkout). This stubs npx with a script that records where it was
// told to write and touches nothing else, so the test proves containment
// without running a real frontend build.
func TestNpmBuildWritesOnlyToOutDir(t *testing.T) {
	stubDir := t.TempDir()
	npxStub := filepath.Join(stubDir, "npx")
	script := "#!/bin/sh\n" +
		"# args: vite build --outDir <outDir> --emptyOutDir\n" +
		"outdir=\"$4\"\n" +
		"mkdir -p \"$outdir\"\n" +
		"touch \"$outdir/index.html\"\n"
	if err := os.WriteFile(npxStub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	sourceDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "dist")
	logPath := filepath.Join(t.TempDir(), "build.log")
	logw, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logw.Close()

	// exec.Command resolves "npx" against the process's real PATH, not
	// cmd.Env, so the stub has to go on the actual PATH too.
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := []string{"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err := npmBuild(context.Background(), sourceDir, outDir, env, logw); err != nil {
		t.Fatalf("npmBuild failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "index.html")); err != nil {
		t.Fatalf("expected the build to land in outDir: %v", err)
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("npmBuild wrote into the source checkout: %v", entries)
	}
}
