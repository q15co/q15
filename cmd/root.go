// Package cmd defines the q15 CLI commands.
package cmd

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "q15",
	Short:         "Q15 agent runtime",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

func init() {
	cobra.OnInitialize(initEnv)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "q15.toml", "path to config file")

	cobra.CheckErr(viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config")))

	rootCmd.AddCommand(startCmd)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.ExecuteContext(context.Background())
}

func initEnv() {
	viper.SetEnvPrefix("Q15")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()
}
