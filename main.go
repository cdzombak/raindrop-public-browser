// Command raindrop-public-browser serves a paginated, searchable browser for
// Raindrop.io bookmarks tagged as public.
package main

import (
	"fmt"
	"log/slog"
	"os"
	_ "time/tzdata" // the scratch Docker image has no timezone database
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "<dev>"

func usage() {
	fmt.Fprintf(os.Stderr, `raindrop-public-browser %s

Usage: %s <command>

Commands:
  login        Run the interactive Raindrop OAuth flow and write the state file
  serve        Run the web server
  healthcheck  Probe the running server's /_status endpoint; exit 0 if up
  version      Print the version
`, version, os.Args[0])
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "login":
		err = runLogin(logger)
	case "serve":
		err = runServe(logger)
	case "healthcheck":
		err = runHealthcheck()
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
