package secret

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// The four commands podman's shell secret driver calls. Their contract is
// podman's, not ours, and it is documented in podman-secret-create(1): the
// commands are installed under `[secrets.opts]`, they are run as
// `<shell> -c <command>`, and every one of them except `list` is handed its
// identifier in SECRET_ID.
const (
	DriverList   = "list"
	DriverLookup = "lookup"
	DriverStore  = "store"
	DriverDelete = "delete"
)

// DriverVerbs is the four commands, in the order podman is told about them.
// They are listed once because two things render them - the drop-in podman
// loads and the options a `podman secret create` command line carries - and a
// verb missing from one of those is a driver that answers three of podman's
// four questions.
var DriverVerbs = []string{DriverList, DriverLookup, DriverStore, DriverDelete}

// SecretIDEnv is the environment variable podman passes a secret's identifier
// in.
const SecretIDEnv = "SECRET_ID"

// RunDriver executes one driver command.
//
// Standard output belongs to podman here, so it carries exactly what the
// contract says and nothing else: one identifier per line for `list`, the value
// for `lookup`, and nothing at all for `store` and `delete`. A stray sentence
// is not a cosmetic problem, because podman parses this stream: an extra line
// becomes a corrupted identifier or a corrupted payload. Everything human has
// to go to standard error, which is why nothing in this function writes to
// standard output except the two commands whose output is the answer.
func RunDriver(ctx context.Context, store *Store, env command.Env, args []string) error {
	env = env.WithDefaults()
	if len(args) != 1 {
		return command.Usagef("secret-driver takes one command, got %d arguments", len(args))
	}

	switch args[0] {
	case DriverList:
		return driverList(store, env)
	case DriverLookup:
		return driverLookup(ctx, store, env)
	case DriverStore:
		return driverStore(ctx, store, env)
	case DriverDelete:
		return driverDelete(store, env)
	default:
		return command.Usagef("unknown secret driver command %q", args[0])
	}
}

// driverList prints one identifier per line, which is all podman reads.
func driverList(store *Store, env command.Env) error {
	ids, err := store.IDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := fmt.Fprintln(env.Stdout, id.ID); err != nil {
			return fmt.Errorf("secret: write the identifier list: %w", err)
		}
	}
	return nil
}

// driverLookup prints the value and nothing else.
func driverLookup(ctx context.Context, store *Store, env command.Env) error {
	id, err := driverID(env)
	if err != nil {
		return err
	}
	value, err := store.Lookup(ctx, id)
	if err != nil {
		return err
	}
	if _, err := io.Copy(env.Stdout, bytes.NewReader(value)); err != nil {
		return fmt.Errorf("secret: write the value: %w", err)
	}
	return nil
}

// driverStore reads the value from standard input.
func driverStore(ctx context.Context, store *Store, env command.Env) error {
	id, err := driverID(env)
	if err != nil {
		return err
	}
	value, err := io.ReadAll(env.Stdin)
	if err != nil {
		return fmt.Errorf("secret: read the value: %w", err)
	}
	return store.Set(ctx, id, value)
}

// driverDelete removes a slot and reports nothing on success. An absent slot is
// a success, and an empty identifier is not: see Store.Delete for why the
// difference decides whether podman destroys an unrelated secret.
func driverDelete(store *Store, env command.Env) error {
	id, err := driverID(env)
	if err != nil {
		return err
	}
	return store.Delete(id)
}

// driverID reads the identifier podman passed in the environment.
func driverID(env command.Env) (string, error) {
	id, ok := env.LookupEnv(SecretIDEnv)
	if !ok || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("secret: %s is not set", SecretIDEnv)
	}
	return id, nil
}
