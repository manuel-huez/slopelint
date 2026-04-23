package main

import (
	"flag"
	"fmt"
	"os"

	"example.com/defenselint/internal/lint"
)

func main() {
	var maxStates int
	flag.IntVar(&maxStates, "max-states", 32, "maximum number of symbolic states before widening")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] [packages]\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Examples:")
		fmt.Fprintf(flag.CommandLine.Output(), "  %s ./...\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "  %s -max-states=64 ./internal/...\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "")
		flag.PrintDefaults()
	}
	flag.Parse()

	packages, err := lint.LoadPackages(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "defenselint: %v\n", err)
		os.Exit(2)
	}

	var all []lint.Issue
	for _, pkg := range packages {
		issues := lint.LintPackage(pkg, lint.Options{MaxStates: maxStates})
		all = append(all, issues...)
	}

	for _, issue := range all {
		fmt.Printf("%s: %s\n", issue.Pos.String(), issue.Message)
	}
	if len(all) > 0 {
		os.Exit(1)
	}
}
