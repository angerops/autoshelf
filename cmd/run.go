package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/angerops/autoshelf/internal/config"
	"github.com/angerops/autoshelf/internal/rules"
	"github.com/angerops/autoshelf/internal/watcher"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Watch configured folders and apply rules in real time",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := newLogger()

		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}
		if dryRun {
			cfg.DryRun = true
		}
		logger.Info("config loaded", "file", cfg.SourceFile(), "watches", len(cfg.Watches), "dry_run", cfg.DryRun)

		// engineFactory is reused on every reload so ApplyConfig can build a
		// fresh engine without internal/watcher needing to import internal/rules.
		// The dryRun CLI flag always wins over what's in the file - same
		// precedence as initial load.
		engineFactory := func(c *config.Config) watcher.Engine {
			if dryRun {
				c.DryRun = true
			}
			return rules.New(c, logger)
		}

		engine := engineFactory(cfg)
		w := watcher.New(cfg, engine, logger)

		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		// Auto-reload: watch the resolved config file for edits. On a
		// successful reload, ApplyConfig hot-swaps rules + reconciles
		// fsnotify watches in place. A failed reload (invalid YAML,
		// validation error) is logged and the daemon keeps running the
		// last good config.
		sourcePath := cfg.SourceFile()
		if sourcePath != "" {
			go func() {
				err := watcher.WatchConfigFile(ctx, sourcePath, 0, func() {
					newCfg, err := config.Load(sourcePath)
					if err != nil {
						logger.Error("config reload failed (keeping current config)",
							"file", sourcePath, "err", err)
						return
					}
					if err := w.ApplyConfig(ctx, newCfg, engineFactory); err != nil {
						logger.Error("apply reloaded config failed (keeping current config)",
							"file", sourcePath, "err", err)
						return
					}
					logger.Info("config reloaded",
						"file", newCfg.SourceFile(),
						"watches", len(newCfg.Watches),
						"dry_run", newCfg.DryRun)
				}, logger)
				if err != nil && !errors.Is(err, context.Canceled) {
					logger.Warn("config watcher stopped", "err", err)
				}
			}()
		}

		if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		logger.Info("shutdown complete")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
