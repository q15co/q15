package cmd

import (
	"fmt"
	"strings"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/spf13/cobra"
)

func newAuthLoginCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Login to a provider using OAuth",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider != supportedAuthProvider {
				return fmt.Errorf(
					"unsupported provider %q (supported providers: %s)",
					provider,
					supportedAuthProvider,
				)
			}

			cred, err := q15auth.LoginOpenAIDeviceCode(cmd.Context(), cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("openai login failed: %w", err)
			}
			if err := q15auth.SetCredential(supportedAuthProvider, cred); err != nil {
				return fmt.Errorf("save openai credential: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Login successful!")
			if cred.AccountID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Account: %s\n", cred.AccountID)
			}
			if !cred.ExpiresAt.IsZero() {
				fmt.Fprintf(
					cmd.OutOrStdout(),
					"Expires at: %s\n",
					cred.ExpiresAt.Format("2006-01-02 15:04:05 -0700"),
				)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(
		&provider,
		"provider",
		"p",
		"",
		"provider to login with (openai)",
	)
	_ = cmd.MarkFlagRequired("provider")

	return cmd
}
