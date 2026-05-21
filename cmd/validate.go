package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/angerops/autoshelf/internal/config"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Parse and validate the config without running",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}
		fmt.Printf("config OK: %s\n", cfg.SourceFile())
		fmt.Printf("  watches:    %d\n", len(cfg.Watches))
		total := 0
		for _, w := range cfg.Watches {
			total += len(w.Rules)
			fmt.Printf("  - %s (%d rules)\n", w.Path, len(w.Rules))
		}
		fmt.Printf("  total rules: %d\n", total)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
