package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/stack"
)

type systemdStack struct {
	env     command.Env
	profile stack.Profile
}

func (s systemdStack) Start(ctx context.Context) error {
	units := s.profile.ServiceNames()

	runner := processRunner{env: s.env}
	fmt.Fprintf(s.env.Stderr, "reloading the user's systemd\n")
	if err := stack.Reload(ctx, runner); err != nil {
		return err
	}

	fmt.Fprintf(s.env.Stderr, "starting %d units\n", len(units))
	if err := switchUnits(ctx, runner, "start", units); err != nil {
		return fmt.Errorf("start the stack: %w", err)
	}
	return nil
}

// switchUnits starts or stops every unit in one systemctl call, so systemd
// orders the transitions itself from the units' own dependencies rather than the
// CLI guessing at an order it would have to keep in step with the renderer.
func switchUnits(ctx context.Context, runner processRunner, action string, units []string) error {
	args := make([]string, 0, len(units)+2)
	args = append(args, "--user", action)
	args = append(args, units...)
	return runner.Run(ctx, "systemctl", args...)
}
func (s systemdStack) Stop(ctx context.Context) error {
	units := s.profile.ServiceNames()

	fmt.Fprintf(s.env.Stderr, "stopping %d units\n", len(units))
	if err := switchUnits(ctx, processRunner{env: s.env}, "stop", units); err != nil {
		return fmt.Errorf("stop the stack: %w", err)
	}
	return nil
}

func (s systemdStack) Status(ctx context.Context) error {
	units := s.profile.ServiceNames()

	unitStates := make(map[string]string, len(units))
	for _, unit := range units {
		out, err := systemctlOutput(ctx, s.env, "--user", "is-active", unit)
		state := strings.TrimSpace(out)
		if state == "" {
			if err != nil {
				return fmt.Errorf("read the state of %s: %w", unit, err)
			}
			state = "unknown"
		}
		unitStates[unit] = state
	}

	containers, err := containersByName(ctx, s.env)
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

	w := tabwriter.NewWriter(s.env.Stdout, 0, 0, 2, ' ', 0)
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
			s.env.Stderr,
			"no unit of the stack is active: run 'q15 up', or 'q15 init' if the stack has never been rendered\n",
		)
	}
	if len(problems) > 0 {
		return fmt.Errorf("the stack is not up: %s", strings.Join(problems, ", "))
	}
	return nil
}

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

func containerUnitName(unit string) string {
	if strings.HasSuffix(unit, "-network.service") {
		return ""
	}
	name, ok := strings.CutSuffix(unit, ".service")
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
func (s systemdStack) Logs(ctx context.Context, service string, follow bool) error {
	units := s.profile.ServiceNames()

	if service != "" {
		unit, err := resolveUnit(s.profile, units, service)
		if err != nil {
			return err
		}
		units = []string{unit}
	}

	journalArgs := make([]string, 0, len(units)*2+2)
	journalArgs = append(journalArgs, "--user")
	for _, unit := range units {
		journalArgs = append(journalArgs, "-u", unit)
	}
	if follow {
		journalArgs = append(journalArgs, "--follow")
	}

	if err := s.env.Exec(ctx, "journalctl", journalArgs, s.env.Stdin, s.env.Stdout, s.env.Stderr); err != nil {
		if command.NotInstalled(err) {
			return fmt.Errorf("journalctl is not installed: %w", err)
		}
		return fmt.Errorf("read the journal: %w", err)
	}
	return nil
}

// resolveUnit turns the word an operator typed into one of the stack's units. A
// service word is the part that makes it recognisable - the name it has in the
// stack, or the same word with the stack's own prefix - and the full unit name
// works too, because that is what status prints and what an operator pastes
// back.
func resolveUnit(profile stack.Profile, units []string, word string) (string, error) {
	if stem, ok := strings.CutSuffix(word, ".container"); ok {
		word = stem + ".service"
	} else if stem, ok := strings.CutSuffix(word, ".network"); ok {
		word = stem + "-network.service"
	}
	prefix := profile.ResourceName() + "-"
	for _, unit := range units {
		stem := strings.TrimSuffix(strings.TrimSuffix(unit, "-network.service"), ".service")
		switch {
		case unit == word, stem == word, stem == prefix+word:
			return unit, nil
		}
	}
	return "", command.Usagef(
		"unknown service %q, want one of %s",
		word,
		strings.Join(serviceWords(profile, units), ", "),
	)
}

// serviceWords are the names resolveUnit accepts, for an error that lists them.
func serviceWords(profile stack.Profile, units []string) []string {
	prefix := profile.ResourceName() + "-"
	words := make([]string, 0, len(units))
	for _, unit := range units {
		stem := strings.TrimSuffix(strings.TrimSuffix(unit, "-network.service"), ".service")
		words = append(words, strings.TrimPrefix(stem, prefix))
	}
	return words
}

// processRunner runs a verb's external programs through the process seam it was
// handed, so reloading the user's systemd is exercised by a test rather than
// only on a real machine. Both streams go to stderr: what systemd says about a
// reload is a diagnostic, and stdout belongs to the verb's own output.
type processRunner struct{ env command.Env }

// Run implements stack.Runner.
func (r processRunner) Run(ctx context.Context, program string, args ...string) error {
	return r.env.Exec(ctx, program, args, nil, r.env.Stderr, r.env.Stderr)
}

// systemctlOutput runs one read-only systemctl query and returns what it wrote
// to standard output. systemctl reports state through its exit status as well as
// through its output, and "inactive" is an answer rather than a failure, so the
// caller reads the output and decides what to make of a non-zero status.
func systemctlOutput(ctx context.Context, env command.Env, args ...string) (string, error) {
	var out strings.Builder
	err := env.Exec(ctx, "systemctl", args, nil, &out, env.Stderr)
	if err != nil && command.NotInstalled(err) {
		return "", fmt.Errorf("systemctl is not installed: %w", err)
	}
	return out.String(), err
}
