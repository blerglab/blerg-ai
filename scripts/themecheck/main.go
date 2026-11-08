// Command themecheck audits the five Blerg frontend source trees for color and theme-ID literals
// that bypass the central theme tokens (BLG-473). Usage:
//
//	go run ./scripts/themecheck <repo-root>
//
// Exits 1 and prints a file:line report if anything is flagged; exits 0 otherwise.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"blerg/scripts/themecheck/audit"
)

// scanDirs are the five trees the card names, relative to the repo root.
var scanDirs = []string{
	"runner/frontend/src",
	"packages/chat/src",
	"packages/board",
	"board/web/src",
	"core/web/src",
}

// assetDirs are each app's static public/ folder: not under src/, but still a real surface (an
// SVG favicon/logo, served and rendered outside React's reach — review finding 9).
var assetDirs = []string{
	"runner/frontend/public",
	"board/web/public",
	"core/web/public",
}

// entryFiles are each app's HTML entry point, scanned directly (not a directory walk) — review
// finding 9 asked for index.html (its theme-color meta) to be in scope too.
var entryFiles = []string{
	"runner/frontend/index.html",
	"board/web/index.html",
	"core/web/index.html",
}

// tokenFiles define the theme contract: every custom property declared in every theme block here
// is a legitimate var() fallback target. They are also exempt from the audit itself (a theme file
// is allowed to name colors; it's the definition, not a consumer).
var tokenFiles = []string{
	"brand/tokens.css",
	"packages/chat/src/theme/brand.css",
	"packages/chat/src/theme/blerg.css",
	"packages/chat/src/theme/tokens.css",
}

// exemptFiles are files that name colors on purpose, outside the brand/dark/light/colorblind/
// high-contrast system: codeThemes.ts is itself a set of theme definitions, the same role as
// tokenFiles above, but for a second, orthogonal, user-chosen axis (which syntax palette a code
// block uses), not the five central themes.
var exemptFiles = append(append([]string{}, tokenFiles...), "packages/chat/src/theme/codeThemes.ts")

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: themecheck <repo-root>")
		os.Exit(2)
	}
	root := os.Args[1]

	tokenContents := map[string]string{}
	for _, f := range tokenFiles {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			fmt.Fprintf(os.Stderr, "themecheck: reading token file %s: %v\n", f, err)
			os.Exit(2)
		}
		tokenContents[f] = string(b)
	}
	everyTheme := audit.EveryThemeTokens(tokenContents, audit.ThemeIDs)

	var violations []audit.Violation
	scanOne := func(root, rel string) {
		if audit.IsExempt(rel, exemptFiles) || audit.IsTestFile(rel) {
			return
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			fmt.Fprintf(os.Stderr, "themecheck: reading %s: %v\n", rel, err)
			os.Exit(2)
		}
		violations = append(violations, audit.ScanFile(rel, string(b), everyTheme)...)
	}
	walkDir := func(dir string, exts map[string]bool) {
		full := filepath.Join(root, dir)
		err := filepath.Walk(full, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !exts[strings.ToLower(filepath.Ext(rel))] {
				return nil
			}
			scanOne(root, rel)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "themecheck: walking %s: %v\n", dir, err)
			os.Exit(2)
		}
	}

	codeExts := map[string]bool{".css": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true}
	for _, dir := range scanDirs {
		walkDir(dir, codeExts)
	}
	svgExt := map[string]bool{".svg": true}
	for _, dir := range assetDirs {
		walkDir(dir, svgExt)
	}
	for _, f := range entryFiles {
		scanOne(root, f)
	}

	if len(violations) == 0 {
		fmt.Println("themecheck: no color or theme-ID literals found outside the central tokens")
		return
	}
	for _, v := range violations {
		fmt.Println(v.String())
	}
	fmt.Fprintf(os.Stderr, "\nthemecheck: %d violation(s). Use an existing theme token, add one every theme "+
		"defines, or add a trailing `themecheck:allow <reason>` comment naming the owning card.\n", len(violations))
	os.Exit(1)
}
