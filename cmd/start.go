package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"q15.co/sandbox/internal/app"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start all configured agents",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return app.Start(cmd.Context(), strings.TrimSpace(viper.GetString("config")))
	},
}
