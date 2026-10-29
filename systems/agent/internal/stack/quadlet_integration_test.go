package stack

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestQuadletGenerator(t *testing.T) {
	generator := os.Getenv("Q15_TEST_QUADLET_GENERATOR")
	if generator == "" {
		t.Skip("set Q15_TEST_QUADLET_GENERATOR to validate with Podman's generator")
	}
	for _, name := range []string{"Q15", "Startup Check"} {
		t.Run(name, func(t *testing.T) {
			profile := testProfile()
			profile.AgentName = name
			dir := t.TempDir()
			if _, err := profile.Render(dir); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, generator, "--user", "--dryrun")
			cmd.Env = append(os.Environ(), "QUADLET_UNIT_DIRS="+dir)
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("Quadlet generator: %v", err)
			}
			for _, service := range []string{"postgres", "proxy", "exec", "qdrant", "agent", "web"} {
				_, generated, ok := strings.Cut(
					string(output),
					"---"+profile.resource(service)+".service---\n",
				)
				if !ok {
					t.Fatalf("no generated %s service in:\n%s", service, output)
				}
				generated, _, _ = strings.Cut(generated, "\n---")
				unit := directives(t, []byte(generated))
				if start := single(t, unit, "ExecStart"); !strings.Contains(
					start,
					"--sdnotify=healthy",
				) ||
					!strings.Contains(start, "--health-cmd") {
					t.Fatalf("%s has no health notification: %s", service, start)
				}
				single(t, unit, "ExecStartPost")
				if service == "web" &&
					!strings.Contains(
						strings.Join(unit["After"], " "),
						profile.resource("agent")+".service",
					) {
					t.Fatalf("web does not wait for agent startup: %v", unit["After"])
				}
			}
		})
	}
}
