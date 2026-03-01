package cmd

import (
	"fmt"
	"slices"
	"strings"
	"time"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
	"github.com/spf13/cobra"
)

func newAuthStatusCommand() *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show current auth status",
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

			if provider != "" {
				cred, err := q15auth.GetCredential(provider)
				if err != nil {
					return err
				}
				if cred == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Provider %q is not logged in.\n", provider)
					return nil
				}
				printCredentialStatus(cmd, provider, cred)
				return nil
			}

			store, err := q15auth.LoadStore()
			if err != nil {
				return err
			}
			if len(store.Credentials) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No authenticated providers.")
				return nil
			}

			providers := make([]string, 0, len(store.Credentials))
			for p := range store.Credentials {
				providers = append(providers, p)
			}
			slices.Sort(providers)
			for _, p := range providers {
				printCredentialStatus(cmd, p, store.Credentials[p])
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(
		&provider,
		"provider",
		"p",
		"",
		"provider to inspect (openai)",
	)

	return cmd
}

func printCredentialStatus(cmd *cobra.Command, provider string, cred *q15auth.Credential) {
	status := "active"
	if cred.IsExpired() {
		status = "expired"
	} else if cred.NeedsRefresh() {
		status = "needs refresh"
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", provider)
	fmt.Fprintf(cmd.OutOrStdout(), "  Method: %s\n", cred.AuthMethod)
	fmt.Fprintf(cmd.OutOrStdout(), "  Status: %s\n", status)
	if cred.AccountID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Account: %s\n", cred.AccountID)
	}
	if !cred.ExpiresAt.IsZero() {
		fmt.Fprintf(
			cmd.OutOrStdout(),
			"  ExpiresAt: %s (%s)\n",
			cred.ExpiresAt.Format("2006-01-02 15:04:05 -0700"),
			time.Until(cred.ExpiresAt).Round(time.Second),
		)
	}
}
