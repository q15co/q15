package cmd

import (
	"fmt"
	"strings"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/spf13/cobra"
)

func newAuthLogoutCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Logout provider authentication",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider != "" && provider != supportedAuthProvider {
				return fmt.Errorf(
					"unsupported provider %q (supported providers: %s)",
					provider,
					supportedAuthProvider,
				)
			}

			if provider == "" {
				if err := q15auth.DeleteAllCredentials(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Logged out from all providers.")
				return nil
			}

			if err := q15auth.DeleteCredential(provider); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged out from %s.\n", provider)
			return nil
		},
	}

	cmd.Flags().StringVarP(
		&provider,
		"provider",
		"p",
		"",
		"provider to logout (openai)",
	)

	return cmd
}
