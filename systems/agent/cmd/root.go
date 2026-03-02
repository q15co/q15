// Package cmd defines the q15 CLI commands.
package cmd

import (
	"context"
	"path/filepath"
	"strings"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	q15paths "github.com/q15co/q15/systems/agent/internal/paths"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	configKey    = "config"
	configDirKey = "config-dir"
	authPathKey  = "auth-path"
)

var (
	cfgFile   string
	configDir string
	authPath  string
)

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

	rootCmd.PersistentFlags().StringVar(
		&cfgFile,
		configKey,
		"",
		"path to config file (default: <config-dir>/config.toml)",
	)
	rootCmd.PersistentFlags().StringVar(
		&configDir,
		configDirKey,
		defaultConfigDir(),
		"base directory for default q15 config/auth files",
	)
	rootCmd.PersistentFlags().StringVar(
		&authPath,
		authPathKey,
		"",
		"path to auth store file (default: <config-dir>/auth.json)",
	)

	cobra.CheckErr(viper.BindPFlag(configKey, rootCmd.PersistentFlags().Lookup(configKey)))
	cobra.CheckErr(viper.BindPFlag(configDirKey, rootCmd.PersistentFlags().Lookup(configDirKey)))
	cobra.CheckErr(viper.BindPFlag(authPathKey, rootCmd.PersistentFlags().Lookup(authPathKey)))

	rootCmd.AddCommand(startCmd, newAuthCommand())
}

func defaultConfigDir() string {
	path, err := q15paths.DefaultConfigDir()
	if err != nil {
		return ""
	}
	return path
}

func resolveConfigPath(path string, dir string) string {
	path = strings.TrimSpace(path)
	if path != "" {
		return path
	}

	dir = strings.TrimSpace(dir)
	if dir == "" {
		return q15paths.ConfigFileName
	}
	return filepath.Join(dir, q15paths.ConfigFileName)
}

func resolveAuthPath(path string, dir string) string {
	path = strings.TrimSpace(path)
	if path != "" {
		return path
	}

	dir = strings.TrimSpace(dir)
	if dir == "" {
		return q15paths.AuthFileName
	}
	return filepath.Join(dir, q15paths.AuthFileName)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.ExecuteContext(context.Background())
}

func initEnv() {
	viper.SetEnvPrefix("Q15")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	resolvedConfigDir := strings.TrimSpace(viper.GetString(configDirKey))
	resolvedConfigPath := resolveConfigPath(viper.GetString(configKey), resolvedConfigDir)
	resolvedAuthPath := resolveAuthPath(viper.GetString(authPathKey), resolvedConfigDir)

	viper.Set(configKey, resolvedConfigPath)
	viper.Set(configDirKey, resolvedConfigDir)
	viper.Set(authPathKey, resolvedAuthPath)

	cobra.CheckErr(q15auth.SetStorePath(resolvedAuthPath))
}
