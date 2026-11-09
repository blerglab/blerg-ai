package main

import (
	"fmt"
	"os"
)

// usage:
//
//	go run . print <id>          print one bundled theme's rendered CSS block
//	go run . print-all           print every bundled theme's block, in picker order
//	go run . print-sessions      print every bundled theme's session-color override block
//	go run . list                print id, name, license and source for every bundled theme
//	go run . apply <repo-root>   regenerate brand/tokens.css and its two copies in place —
//	                             the only supported way to add, remove or change a bundled theme
func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "list":
		for _, b := range BundledThemes {
			fmt.Printf("%s\t%s\t%s\t%s\n", b.ID, b.Name, b.License, b.Source)
		}
	case "print":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: themeimport print <id>")
			os.Exit(2)
		}
		b, ok := find(os.Args[2])
		if !ok {
			fmt.Fprintf(os.Stderr, "themeimport: unknown bundled theme %q\n", os.Args[2])
			os.Exit(2)
		}
		sch, err := loadBundled(b)
		if err != nil {
			fmt.Fprintln(os.Stderr, "themeimport:", err)
			os.Exit(1)
		}
		fmt.Print(renderBlock(b.ID, mapTokens(sch)))
	case "print-all":
		body, err := renderThemesSection()
		if err != nil {
			fmt.Fprintln(os.Stderr, "themeimport:", err)
			os.Exit(1)
		}
		fmt.Print(body)
	case "print-sessions":
		body, err := renderSessionsSection()
		if err != nil {
			fmt.Fprintln(os.Stderr, "themeimport:", err)
			os.Exit(1)
		}
		fmt.Print(body)
	case "apply":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: themeimport apply <repo-root>")
			os.Exit(2)
		}
		if err := applyToRepo(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "themeimport:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: themeimport print <id> | print-all | print-sessions | list | apply <repo-root>")
	os.Exit(2)
}

func find(id string) (Bundled, bool) {
	for _, b := range BundledThemes {
		if b.ID == id {
			return b, true
		}
	}
	return Bundled{}, false
}
