package cmd

import (
	"github.com/spf13/cobra"

	"github.com/angerops/autoshelf/internal/config"
	"github.com/angerops/autoshelf/internal/rules"
)

var onceCmd = &cobra.Command{
	Use:   "once",
	Short: "Scan watched folders once, apply rules, then exit",
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

		engine := rules.New(cfg, logger)
		matched, scanned, deferred, err := engine.ScanOnce()
		if err != nil {
			return err
		}
		logger.Info("scan complete",
			"scanned", scanned,
			"matched", matched,
			"deferred", len(deferred))
		// In `once` mode there's no daemon to come back and re-check these,
		// so we just surface them as INFO and exit. They'll be picked up
		// next time the entry's age clears its rule's min_age threshold.
		for _, d := range deferred {
			logger.Info("deferred (min_age not yet met)",
				"path", d.Path,
				"retry_at", d.RetryAt)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(onceCmd)
}
