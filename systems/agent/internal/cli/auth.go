package cli

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/authcli"
	"github.com/q15co/q15/systems/agent/internal/command"
)

// runAuth reaches the auth verbs, which predate this binary as the q15-auth
// tool. They are registered as one verb so the operator has one command to
// learn and one artefact to install, and the implementation stays in authcli
// rather than being reimplemented here.
func runAuth(ctx context.Context, env command.Env, args []string) error {
	return authcli.Run(ctx, env, args)
}
