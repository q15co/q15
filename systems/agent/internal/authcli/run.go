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
	"github.com/q15co/q15/systems/agent/internal/command"
)

const (
	loginCommand  = "login"
	logoutCommand = "logout"
	statusCommand = "status"
)

var errHelpRequested = errors.New("help requested")

// Run executes the q15-auth command-line interface.
//
// env carries the streams, so the package writes where it is told rather than
// to os.Stdout. A bare invocation prints the usage to stdout and reports
// success, because asking what a command can do is a request for help rather
// than a mistake; an unrecognised verb prints the same usage to stderr and
// returns a usage error, so the process exits 2 instead of 1.
func Run(ctx context.Context, env command.Env, args []string) error {
	env = env.WithDefaults()

	if len(args) == 0 {
		printUsage(env.Stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case loginCommand:
		return runLogin(ctx, env, args[1:])
	case logoutCommand:
		return runLogout(env, args[1:])
	case statusCommand:
		return runStatus(env, args[1:])
	case "help", "-h", "--help":
		printUsage(env.Stdout)
		return nil
	default:
		printUsage(env.Stderr)
		return command.Usagef("unknown command %q", args[0])
	}
}

func runLogin(ctx context.Context, env command.Env, args []string) error {
	authPath, err := parseAuthPath(loginCommand, args, env.Stderr)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := q15auth.SetStorePath(authPath); err != nil {
		return err
	}

	cred, err := q15auth.LoginOpenAIDeviceCode(ctx, env.Stdout)
	if err != nil {
		return fmt.Errorf("openai login failed: %w", err)
	}
	if err := q15auth.SetCredential("openai", cred); err != nil {
		return fmt.Errorf("save openai credential: %w", err)
	}

	fmt.Fprintln(env.Stdout, "Login successful.")
	if cred.AccountID != "" {
		fmt.Fprintf(env.Stdout, "Account: %s\n", cred.AccountID)
	}
	if !cred.ExpiresAt.IsZero() {
		fmt.Fprintf(
			env.Stdout,
			"Expires at: %s\n",
			cred.ExpiresAt.Format("2006-01-02 15:04:05 -0700"),
		)
	}
	return nil
}

func runLogout(env command.Env, args []string) error {
	authPath, err := parseAuthPath(logoutCommand, args, env.Stderr)
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
	fmt.Fprintln(env.Stdout, "Logged out.")
	return nil
}

func runStatus(env command.Env, args []string) error {
	authPath, err := parseAuthPath(statusCommand, args, env.Stderr)
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
		fmt.Fprintln(env.Stdout, "No authenticated providers.")
		return nil
	}

	providers := make([]string, 0, len(store.Credentials))
	for provider := range store.Credentials {
		providers = append(providers, provider)
	}
	slices.Sort(providers)
	for _, provider := range providers {
		printCredentialStatus(env.Stdout, provider, store.Credentials[provider])
	}
	return nil
}

// parseAuthPath reads the shared flag every verb takes. A flag the flag package
// rejects is marked as a usage mistake rather than passed through as a failure,
// so an operator who mistypes a flag gets the same exit status as one who
// mistypes a verb.
func parseAuthPath(verb string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	authPath := fs.String("auth-path", defaultAuthPath(), "path to auth.json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", errHelpRequested
		}
		return "", command.AsUsage(err)
	}
	if fs.NArg() != 0 {
		return "", command.Usagef("%s does not accept positional arguments", verb)
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
