package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// podmanContainer is the part of podman's container list this verb reads. podman
// renders a container's name as an array in its JSON output and as a bare string
// in older ones, so both fields are read and either answers.
type podmanContainer struct {
	Names []string `json:"Names"`
	Name  string   `json:"Name"`
	State string   `json:"State"`
}

// containerName is the name podman reported for this container, whichever field
// carried it.
func (c podmanContainer) containerName() string {
	if len(c.Names) > 0 {
		return c.Names[0]
	}
	return c.Name
}

// runStatus reports what is running: for each unit of the stack, what systemd
// says about it, and what podman says about the container that unit owns.
//
// It reads and never writes. `systemctl --user is-active` and `podman ps` are
// both read-only, nothing here opens the podman socket and nothing links the
// container bindings, which is the constraint the whole install slice keeps.
//
// A stack that is only partly up is reported unit by unit rather than collapsed
// into one verdict, because the question being asked is which part is down. A
// unit whose container is missing and a unit that systemd has stopped are
// different failures, and the table shows both.
func runStatus(ctx context.Context, env command.Env, args []string) error {
	flags := stackFlags{}
	fs := newStackFlagSet("status", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("status takes no arguments, got %q", fs.Arg(0))
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}
	units := profile.UnitNames()

	unitStates := make(map[string]string, len(units))
	for _, unit := range units {
		out, err := systemctlOutput(ctx, env, "--user", "is-active", unit)
		state := strings.TrimSpace(out)
		if state == "" {
			if err != nil {
				return fmt.Errorf("read the state of %s: %w", unit, err)
			}
			state = "unknown"
		}
		unitStates[unit] = state
	}

	containers, err := containersByName(ctx, env)
	if err != nil {
		return err
	}

	type row struct{ unit, systemd, container string }
	rows := make([]row, 0, len(units))
	problems := make([]string, 0, len(units))
	active := 0

	for _, unit := range units {
		r := row{unit: unit, systemd: unitStates[unit], container: "-"}
		if r.systemd == "active" {
			active++
		} else {
			problems = append(problems, unit+" is "+r.systemd)
		}

		if name := containerUnitName(unit); name != "" {
			state, ok := containers[name]
			switch {
			case !ok:
				r.container = "absent"
				problems = append(problems, name+" has no container")
			default:
				r.container = state
				if state != "running" {
					problems = append(problems, name+" is "+state)
				}
			}
		}
		rows = append(rows, r)
	}

	w := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "UNIT\tSYSTEMD\tCONTAINER")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.unit, r.systemd, r.container)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// A stopped unit and a unit that was never rendered both read as inactive,
	// because that is what systemd itself reports for a name it does not have.
	// Saying so is the difference between an operator reloading the stack and an
	// operator rendering it.
	if active == 0 {
		fmt.Fprint(
			env.Stderr,
			"no unit of the stack is active: run 'q15 up', or 'q15 init' if the stack has never been rendered\n",
		)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the stack is not up: %s", strings.Join(problems, ", "))
	}
	return nil
}

// containerUnitName is the container a unit owns. A container unit's file name
// without its suffix is the ContainerName the renderer gave it, and the network
// unit owns no container, which is what the dash in its row means.
func containerUnitName(unit string) string {
	name, ok := strings.CutSuffix(unit, ".container")
	if !ok {
		return ""
	}
	return name
}

// containersByName lists the stack's containers and their states. It asks podman
// for every container, not only the running ones, because a container that
// exited is the answer to the question being asked rather than something to
// filter out.
func containersByName(ctx context.Context, env command.Env) (map[string]string, error) {
	var out bytes.Buffer
	err := env.Exec(ctx, "podman", []string{"ps", "-a", "--format", "json"}, nil, &out, env.Stderr)
	if err != nil {
		if command.NotInstalled(err) {
			return nil, fmt.Errorf("podman is not installed: %w", err)
		}
		return nil, fmt.Errorf("list the containers: %w", err)
	}

	raw := strings.TrimSpace(out.String())
	if raw == "" {
		return map[string]string{}, nil
	}

	var containers []podmanContainer
	if err := json.Unmarshal([]byte(raw), &containers); err != nil {
		return nil, fmt.Errorf("read podman's container list: %w", err)
	}

	states := make(map[string]string, len(containers))
	for _, container := range containers {
		if name := container.containerName(); name != "" {
			states[name] = container.State
		}
	}
	return states, nil
}
