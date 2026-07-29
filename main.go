// Command raindrop-public-browser serves a paginated, searchable browser for
// Raindrop.io bookmarks tagged as public.
package main

import (
	"fmt"
	"log/slog"
	"os"
	_ "time/tzdata" // the scratch Docker image has no timezone database

	"github.com/cdzombak/raindrop-public-browser/internal/config"
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
	// Build the logger before anything else, so a bad LOG_LEVEL is reported
	// through the same channel as every other error. An invalid value falls
	// back to info rather than refusing to start: losing the ability to run
	// over a logging preference would be a poor trade.
	level, levelErr := config.LogLevel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if levelErr != nil {
		logger.Warn("falling back to the default log level", "error", levelErr)
	}

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
