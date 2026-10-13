package cli

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/stack"
)

type lifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
	Status(context.Context) error
	Logs(context.Context, string, bool) error
}

func lifecycleFor(profile stack.Profile, env command.Env) lifecycle {
	return systemdStack{profile: profile, env: env}
}
