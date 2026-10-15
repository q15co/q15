package cognition

import (
	"context"
	"strings"
	"testing"
	"time"
)

type loopLifecycleTestLoader struct {
	*spyLoader
	files map[string]string
}

func (l loopLifecycleTestLoader) ReadFileBytes(
	_ context.Context,
	path string,
) ([]byte, bool, error) {
	raw, ok := l.files[path]
	return []byte(raw), ok, nil
}

func TestDueOpenLoopMustMoveToLiveWorkingSection(t *testing.T) {
	loop := "- [l-2026-0001] Check the release.\n  {trigger: date:2026-10-15, owner: agent, src: t1}\n"
	spec := Spec{
		RunAt:    time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC),
		Baseline: map[string]string{openLoopsRuntimePath: loop},
	}
	for _, tc := range []struct {
		name, working, loops string
		wantErr              bool
	}{
		{"promoted", "# Working Memory\n## Active Tasks\n- [l-2026-0001] Check the release.\n", "", false},
		{"pending check", "# Working Memory\n## Pending Checks\n- [l-2026-0001] Check the release.\n", "", false},
		{"left due", "# Working Memory\n", loop, true},
		{"silently removed", "# Working Memory\n", "", true},
		{"wrong section", "# Working Memory\n## Recent Progress\n- [l-2026-0001] Check the release.\n", "", true},
		{"identity lost", "# Working Memory\n## Active Tasks\n- Check the release.\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := loopLifecycleTestLoader{
				spyLoader: &spyLoader{},
				files: map[string]string{
					workingMemoryRelativePath: tc.working,
					"working/OPEN_LOOPS.md":   tc.loops,
				},
			}
			err := checkOpenLoopLifecycle(context.Background(), l, spec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("lifecycle error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
	spec.RunAt = spec.RunAt.AddDate(0, 0, -1)
	l := loopLifecycleTestLoader{
		spyLoader: &spyLoader{},
		files:     map[string]string{"working/OPEN_LOOPS.md": loop},
	}
	if err := checkOpenLoopLifecycle(context.Background(), l, spec); err != nil {
		t.Fatalf("future loop: %v", err)
	}
}

func TestWorkingMemoryPromptCarriesExplicitTime(t *testing.T) {
	now := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	spec, err := (workingMemoryConsolidationJob{now: func() time.Time { return now }}).Build(
		context.Background(),
		&workingMemoryJobLoader{},
	)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := renderPrompt(workingMemoryConsolidationJobType, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "2026-10-15T04:00:00Z") || !spec.RunAt.Equal(now) {
		t.Fatalf("missing authoritative run time")
	}
}
