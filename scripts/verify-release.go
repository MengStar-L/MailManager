package main

import (
	"fmt"
	"os"

	"mailmanager/internal/updater"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: verify-release <binary> <version> <goos> <goarch>")
		os.Exit(2)
	}
	if err := updater.VerifyCandidateBuildInfo(os.Args[1], os.Args[2], os.Args[3], os.Args[4]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
