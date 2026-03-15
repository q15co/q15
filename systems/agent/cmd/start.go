package cmd

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/app"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start configured agent",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return app.Start(cmd.Context(), strings.TrimSpace(viper.GetString("config")))
	},
}
