// Package authcli provides the interactive q15-auth bootstrap commands.
package authcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	q15auth "github.com/q15co/q15/systems/agent/internal/auth"
)

const (
	loginCommand  = "login"
	logoutCommand = "logout"
	statusCommand = "status"
)

var errHelpRequested = errors.New("help requested")

// Run executes the q15-auth command-line interface.
func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 0 {
		printUsage(stderr)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case loginCommand:
		return runLogin(ctx, args[1:], stdout, stderr)
	case logoutCommand:
		return runLogout(args[1:], stdout, stderr)
	case statusCommand:
		return runStatus(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runLogin(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	authPath, err := parseAuthPath(loginCommand, args, stderr)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := q15auth.SetStorePath(authPath); err != nil {
		return err
	}

	cred, err := q15auth.LoginOpenAIDeviceCode(ctx, stdout)
	if err != nil {
		return fmt.Errorf("openai login failed: %w", err)
	}
	if err := q15auth.SetCredential("openai", cred); err != nil {
		return fmt.Errorf("save openai credential: %w", err)
	}

	fmt.Fprintln(stdout, "Login successful.")
	if cred.AccountID != "" {
		fmt.Fprintf(stdout, "Account: %s\n", cred.AccountID)
	}
	if !cred.ExpiresAt.IsZero() {
		fmt.Fprintf(stdout, "Expires at: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04:05 -0700"))
	}
	return nil
}

func runLogout(args []string, stdout io.Writer, stderr io.Writer) error {
	authPath, err := parseAuthPath(logoutCommand, args, stderr)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := q15auth.SetStorePath(authPath); err != nil {
		return err
	}
	if err := q15auth.DeleteAllCredentials(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Logged out.")
	return nil
}

func runStatus(args []string, stdout io.Writer, stderr io.Writer) error {
	authPath, err := parseAuthPath(statusCommand, args, stderr)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := q15auth.SetStorePath(authPath); err != nil {
		return err
	}

	store, err := q15auth.LoadStore()
	if err != nil {
		return err
	}
	if len(store.Credentials) == 0 {
		fmt.Fprintln(stdout, "No authenticated providers.")
		return nil
	}

	providers := make([]string, 0, len(store.Credentials))
	for provider := range store.Credentials {
		providers = append(providers, provider)
	}
	slices.Sort(providers)
	for _, provider := range providers {
		printCredentialStatus(stdout, provider, store.Credentials[provider])
	}
	return nil
}

func parseAuthPath(command string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	authPath := fs.String("auth-path", defaultAuthPath(), "path to auth.json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", errHelpRequested
		}
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("%s does not accept positional arguments", command)
	}
	return strings.TrimSpace(*authPath), nil
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  q15-auth login [--auth-path PATH]")
	fmt.Fprintln(out, "  q15-auth status [--auth-path PATH]")
	fmt.Fprintln(out, "  q15-auth logout [--auth-path PATH]")
}

func printCredentialStatus(out io.Writer, provider string, cred *q15auth.Credential) {
	status := "active"
	if cred.IsExpired() {
		status = "expired"
	} else if cred.NeedsRefresh() {
		status = "needs refresh"
	}

	fmt.Fprintf(out, "%s:\n", provider)
	fmt.Fprintf(out, "  Method: %s\n", cred.AuthMethod)
	fmt.Fprintf(out, "  Status: %s\n", status)
	if cred.AccountID != "" {
		fmt.Fprintf(out, "  Account: %s\n", cred.AccountID)
	}
	if !cred.ExpiresAt.IsZero() {
		fmt.Fprintf(
			out,
			"  ExpiresAt: %s (%s)\n",
			cred.ExpiresAt.Format("2006-01-02 15:04:05 -0700"),
			time.Until(cred.ExpiresAt).Round(time.Second),
		)
	}
}

func defaultAuthPath() string {
	path, err := q15auth.DefaultStorePath()
	if err != nil {
		return ""
	}
	return path
}
