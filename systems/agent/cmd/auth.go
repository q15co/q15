package cmd

import "github.com/spf13/cobra"

const supportedAuthProvider = "openai"

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "auth",
		Short:         "Manage provider authentication",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newAuthLoginCommand(),
		newAuthStatusCommand(),
		newAuthLogoutCommand(),
	)

	return cmd
}
