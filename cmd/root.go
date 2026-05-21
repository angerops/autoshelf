package cmd

import (
	"log/slog"
	"os"

	charmlog "github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	verbose bool
	dryRun  bool
)

// version is the user-visible build version, surfaced via --version. The
// default "dev" is overridden at build time via:
//
//	go build -ldflags "-X github.com/angerops/autoshelf/cmd.version=v0.1.0"
//
// The Makefile and Homebrew formula both inject this from the release tag.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "autoshelf",
	Short: "Your files, self-organized.",
	Long: `autoshelf watches folders you choose and moves files into the right place
based on rules you set in a YAML config. Set a rule once, never think about it again.`,
	Version: version,
}

// Execute is the entrypoint called by main.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to config file (default search: ./autoshelf.yaml | ~/.config/autoshelf/autoshelf.yaml | /etc/autoshelf/autoshelf.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging (debug level)")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "log intended moves without performing them (overrides config)")
}

// newLogger returns a slog.Logger backed by charmbracelet/log.
//
// When stderr is a real terminal (interactive `autoshelf run` for example)
// the text formatter is used and charm log adds colors. When stderr is NOT
// a terminal - the launchd / systemd case where stderr is redirected to a
// file - we switch to JSON. JSON in the file is the trade-off that lets
// `autoshelf log` parse each record back into structured form and re-render
// it through a TTY-aware charm log handler on stdout. Plain text in the file
// would force `autoshelf log` to either regex-parse the format (fragile) or
// just cat it (no colors).
func newLogger() *slog.Logger {
	level := charmlog.InfoLevel
	if verbose {
		level = charmlog.DebugLevel
	}
	opts := charmlog.Options{
		Level:           level,
		ReportTimestamp: true,
		TimeFormat:      "2006-01-02T15:04:05.000Z07:00",
	}
	if !isTerminal(os.Stderr) {
		opts.Formatter = charmlog.JSONFormatter
	}
	return slog.New(charmlog.NewWithOptions(os.Stderr, opts))
}

// isTerminal reports whether f is connected to a character device, the same
// check most CLI tools use to decide whether to colorize. Avoids pulling in
// go-isatty for what is effectively a one-line Stat check.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
