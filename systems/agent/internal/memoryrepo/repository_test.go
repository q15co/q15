package memoryrepo

import (
	"context"
	"path/filepath"
	"testing"
)

type recordingCommitter struct {
	ensureCalls int
	commitCalls int
	resetCalls  int
	root        string
	message     string
	paths       []string
}

func (c *recordingCommitter) EnsureRepo(_ context.Context, root string) error {
	c.ensureCalls++
	c.root = root
	return nil
}

func (c *recordingCommitter) CommitPaths(
	_ context.Context,
	root string,
	message string,
	paths []string,
) error {
	c.commitCalls++
	c.root = root
	c.message = message
	c.paths = append([]string(nil), paths...)
	return nil
}

func (c *recordingCommitter) ResetPaths(
	_ context.Context,
	root string,
	paths []string,
) error {
	c.resetCalls++
	c.root = root
	c.paths = append([]string(nil), paths...)
	return nil
}

func TestRepositoryInitAndCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	committer := &recordingCommitter{}
	repository := New(root, committer)

	if err := repository.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if committer.ensureCalls != 1 || committer.root != root {
		t.Fatalf("EnsureRepo() calls = %d, root = %q", committer.ensureCalls, committer.root)
	}

	release := repository.Acquire()
	err := repository.Commit(context.Background(), "state: test", "domain/jobs/job.json")
	release()
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if committer.commitCalls != 1 ||
		committer.message != "state: test" ||
		len(committer.paths) != 1 ||
		committer.paths[0] != "domain/jobs/job.json" {
		t.Fatalf(
			"Commit() calls = %d, message = %q, paths = %q",
			committer.commitCalls,
			committer.message,
			committer.paths,
		)
	}
}

func TestRepositoryInitRejectsRelativeRoot(t *testing.T) {
	repository := New("relative", &recordingCommitter{})
	if err := repository.Init(context.Background()); err == nil {
		t.Fatal("Init() error = nil, want relative-root error")
	}
}

func TestRepositoryInitRejectsEmptyRoot(t *testing.T) {
	repository := New(" ", &recordingCommitter{})
	if err := repository.Init(context.Background()); err == nil {
		t.Fatal("Init() error = nil, want empty-root error")
	}
}

func TestRepositoryCommitRejectsEscapingPath(t *testing.T) {
	repository := New(t.TempDir(), &recordingCommitter{})
	release := repository.Acquire()
	err := repository.Commit(context.Background(), "state: test", "../escape")
	release()
	if err == nil {
		t.Fatal("Commit() error = nil, want escaping-path error")
	}
}
