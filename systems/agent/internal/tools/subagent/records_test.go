package subagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

func persistentTestManager(t *testing.T, root string, model agent.ModelClient) *Manager {
	t.Helper()
	manager := newTestManager(t, model)
	if err := manager.Restore(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func TestSessionRecordsSurviveRestart(t *testing.T) {
	for _, state := range []string{"completed", "failed", "killed"} {
		t.Run(state, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			model := &recordingModel{complete: func(ctx context.Context, _ string,
				_ []conversation.Message, _ []agent.ToolDefinition, _ int,
			) (agent.ModelClientResult, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return agent.ModelClientResult{}, ctx.Err()
				}
				if state == "failed" {
					return agent.ModelClientResult{}, errors.New("model failed")
				}
				return assistantResult("durable answer"), nil
			}}
			root := filepath.Join(t.TempDir(), "subagent")
			manager := persistentTestManager(t, root, model)
			ctx := turnctx.WithTurnSeq(t.Context(), 42)
			out, err := NewSpawn(
				manager,
			).Run(ctx, `{"model":"child","task":"durable task","wait_seconds":0}`)
			if err != nil {
				t.Fatal(err)
			}
			sessions := manager.List("running", 0)
			if len(sessions) != 1 || !strings.Contains(out, sessions[0].ID) {
				t.Fatalf("spawned sessions = %v, output = %q", sessions, out)
			}
			session := sessions[0]
			<-started
			record, err := readSessionRecord(session.recordPath)
			if err != nil || record.State != "running" || record.ParentTurn != 42 ||
				record.RunID == "" {
				t.Fatalf("initial durable record = %+v, error = %v", record, err)
			}
			if state == "killed" {
				session.Kill()
			} else {
				close(release)
			}
			waitDone(t, session)
			record, err = readSessionRecord(session.recordPath)
			if err != nil || record.State != state || record.FinishedAt.IsZero() ||
				record.LastEventIndex != record.EventCount-1 {
				t.Fatalf("terminal durable record = %+v, error = %v", record, err)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			restarted := persistentTestManager(t, root, &recordingModel{})
			restored, ok := restarted.Get(session.ID)
			if !ok || restored.status() != state {
				t.Fatalf("restored session = %v, found = %t", restored, ok)
			}
			if state == "completed" &&
				!strings.Contains(
					restored.Read(0, defaultMaxOutput),
					"Final-Answer: durable answer",
				) {
				t.Fatal("restart lost the final answer")
			}
			if state == "failed" &&
				!strings.Contains(restored.Read(0, defaultMaxOutput), "model failed") {
				t.Fatal("restart lost the failure reason")
			}
			next, err := restarted.Start(StartRequest{ModelRef: "child", Task: "new task"})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, next)
			if next.ID == session.ID || next.runID == session.runID {
				t.Fatal("session identity was reused after restart")
			}
		})
	}
}

func TestSessionRecordsEnforceSingleWriter(t *testing.T) {
	root := t.TempDir()
	first := persistentTestManager(t, root, &recordingModel{})
	second := newTestManager(t, &recordingModel{})
	if err := second.Restore(root); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("concurrent Restore() error = %v, want a lock failure", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Restore(root); err != nil {
		t.Fatalf("Restore() after owner exits: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRecordBoundsDiagnosticTail(t *testing.T) {
	root := t.TempDir()
	manager := persistentTestManager(t, root, &recordingModel{})
	session := &Session{
		ID: "subagent-large", Model: "child", Task: strings.Repeat("task ", 10000),
		Status: "running", CreatedAt: time.Now(), runID: "run-large",
	}
	for i := 0; i < 100; i++ {
		session.addEventLocked("tool_finished", strings.Repeat("output ", 10000), nil)
	}
	if err := manager.records.save(session.recordLocked()); err != nil {
		t.Fatal(err)
	}
	record, err := readSessionRecord(manager.records.path(session.ID))
	if err != nil || len(record.Tail) != maxRecordTailEvents || record.LastEventIndex != 99 {
		t.Fatalf("bounded record = %+v, error = %v", record, err)
	}
	info, err := os.Stat(manager.records.path(session.ID))
	if err != nil || info.Size() > 64*1024 || info.Mode().Perm() != 0o600 {
		t.Fatalf("record metadata = %v, error = %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tmp-abandoned"), []byte("partial JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, &recordingModel{})
	restored, ok := restarted.Get(session.ID)
	if !ok {
		t.Fatal("bounded diagnostic record disappeared")
	}
	read := restored.Read(0, 512)
	if len(read) > 512 || !strings.Contains(read, "Next-Event-Index: 101") {
		t.Fatalf("restored read exceeded its budget or lost its cursor: %q", read)
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp-abandoned")); !os.IsNotExist(err) {
		t.Fatalf("interrupted temporary write survived recovery: %v", err)
	}
}

func TestSessionRecordWriteFailurePreventsLaunch(t *testing.T) {
	model := &recordingModel{}
	root := filepath.Join(t.TempDir(), "subagent")
	manager := persistentTestManager(t, root, model)
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(StartRequest{ModelRef: "child", Task: "must not run"}); err == nil {
		t.Fatal("Start() succeeded without a durable record")
	}
	if len(model.snapshotCalls()) != 0 || len(manager.List("all", 0)) != 0 {
		t.Fatal("a session was launched after its initial record failed")
	}
}

func TestSessionTerminalRecordFailureIsVisible(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	model := &recordingModel{complete: func(ctx context.Context, _ string,
		_ []conversation.Message, _ []agent.ToolDefinition, _ int,
	) (agent.ModelClientResult, error) {
		close(started)
		select {
		case <-release:
			return assistantResult("answer"), nil
		case <-ctx.Done():
			return agent.ModelClientResult{}, ctx.Err()
		}
	}}
	root := filepath.Join(t.TempDir(), "subagent")
	manager := persistentTestManager(t, root, model)
	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitDone(t, session)
	if session.status() != "failed" ||
		!strings.Contains(session.Read(0, defaultMaxOutput), "persistence_failed") {
		t.Fatalf("terminal persistence failure was hidden: %s", session.Read(0, defaultMaxOutput))
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root+"-offline", root); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, &recordingModel{})
	restored, ok := restarted.Get(session.ID)
	if !ok || restored.status() != "interrupted" ||
		!strings.Contains(restored.Read(0, defaultMaxOutput), "lost to restart") {
		t.Fatalf("uncommitted terminal record was treated as success: %v", restored)
	}
}

func TestRestoreRejectsInvalidRecordsWithoutDeletingThem(t *testing.T) {
	for _, content := range []string{`{"schema_version":99}`, `{broken json`} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "subagent-corrupt.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(t, &recordingModel{})
			if err := manager.Restore(root); err == nil {
				t.Fatal("Restore() accepted an invalid record")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != content {
				t.Fatalf("invalid record changed: %q, %v", data, err)
			}
		})
	}
}

func TestRecordRetentionBoundsDiskAndPreservesRunningSessions(t *testing.T) {
	started := make(chan struct{})
	model := &recordingModel{complete: func(ctx context.Context, _ string,
		_ []conversation.Message, _ []agent.ToolDefinition, call int,
	) (agent.ModelClientResult, error) {
		if call == 1 {
			close(started)
			<-ctx.Done()
			return agent.ModelClientResult{}, ctx.Err()
		}
		return assistantResult("retained answer"), nil
	}}
	root := t.TempDir()
	manager := persistentTestManager(t, root, model)
	running, err := manager.Start(StartRequest{ModelRef: "child", Task: "still running"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	var oldest string
	for i := 0; i < maxFinishedSessionRecords+2; i++ {
		session, err := manager.Start(
			StartRequest{ModelRef: "child", Task: fmt.Sprintf("task %d", i)},
		)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = session.ID
		}
		waitDone(t, session)
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil || len(paths) != maxFinishedSessionRecords+1 {
		t.Fatalf("record count = %d, error = %v", len(paths), err)
	}
	if _, err := os.Stat(filepath.Join(root, oldest+".json")); !os.IsNotExist(err) {
		t.Fatalf("oldest record survived eviction: %v", err)
	}
	record, err := readSessionRecord(running.recordPath)
	if err != nil || record.State != "running" {
		t.Fatalf("running record was evicted: %+v, %v", record, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := persistentTestManager(t, root, &recordingModel{})
	next, err := restarted.Start(StartRequest{ModelRef: "child", Task: "after retention"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, next)
	if !strings.HasSuffix(next.ID, fmt.Sprintf("-%d", maxFinishedSessionRecords+4)) {
		t.Fatalf("retention reset the durable sequence: %q", next.ID)
	}
}

func TestSessionRecoveryAfterProcessExit(t *testing.T) {
	for _, mode := range []string{"kill", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(
				t.Context(),
				executable,
				"-test.run=^TestSubagentRecordProcessHelper$",
			)
			command.Env = append(os.Environ(), "Q15_SUBAGENT_RECORD_HELPER="+root)
			command.Stderr = os.Stderr
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = command.Process.Kill() })
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				if scanner.Scan() {
					ready <- scanner.Text()
				}
			}()
			var id string
			select {
			case id = <-ready:
			case <-time.After(30 * time.Second):
				t.Fatal("helper did not reach a durable progress checkpoint")
			}
			before, err := readSessionRecord(filepath.Join(root, id+".json"))
			if err != nil || before.ParentTurn != 42 || before.State != "running" ||
				len(before.Tail) == 0 {
				t.Fatalf("running checkpoint = %+v, error = %v", before, err)
			}
			if mode == "kill" {
				err = command.Process.Kill()
			} else {
				err = command.Process.Signal(syscall.SIGTERM)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); mode == "shutdown" && err != nil {
				t.Fatal(err)
			}
			restarted := persistentTestManager(t, root, &recordingModel{})
			session, ok := restarted.Get(id)
			if !ok || session.status() != "interrupted" {
				t.Fatalf("recovered session = %v, found = %t", session, ok)
			}
			reason := "lost to restart"
			if mode == "shutdown" {
				reason = "shutdown"
			}
			read := session.Read(0, defaultMaxOutput)
			for _, want := range []string{reason, "tool_finished", "Parent-Turn: 42", "Record-Path:", "restored snapshot"} {
				if !strings.Contains(read, want) {
					t.Fatalf("recovered read = %q, want %q", read, want)
				}
			}
			list, err := NewList(restarted).Run(t.Context(), `{"state":"interrupted"}`)
			if err != nil || !strings.Contains(list, id) || !strings.Contains(list, reason) {
				t.Fatalf("recovered list = %q, error = %v", list, err)
			}
			if err := session.Write("resume"); err == nil {
				t.Fatal("an interrupted session accepted new work")
			}
			terminal, err := readSessionRecord(session.recordPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Close(); err != nil {
				t.Fatal(err)
			}
			again := persistentTestManager(t, root, &recordingModel{})
			reloaded, ok := again.Get(id)
			if !ok || reloaded.droppedEvents+len(reloaded.events) != terminal.EventCount ||
				!reloaded.finishedAt.Equal(terminal.FinishedAt) {
				t.Fatal("a second restart reconciled an already interrupted session again")
			}
			next, err := again.Start(StartRequest{ModelRef: "child", Task: "after process exit"})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, next)
			if !strings.HasSuffix(id, "-1") || !strings.HasSuffix(next.ID, "-2") {
				t.Fatalf("process exit reset the sequence: %q, %q", id, next.ID)
			}
		})
	}
}

func TestSubagentRecordProcessHelper(t *testing.T) {
	root := os.Getenv("Q15_SUBAGENT_RECORD_HELPER")
	if root == "" {
		return
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	defer signal.Stop(stop)
	started := make(chan struct{})
	model := &recordingModel{complete: func(ctx context.Context, _ string,
		_ []conversation.Message, _ []agent.ToolDefinition, call int,
	) (agent.ModelClientResult, error) {
		if call == 1 {
			return agent.ModelClientResult{Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.ToolCall("read", "read_file", `{}`)),
			}}, nil
		}
		close(started)
		<-ctx.Done()
		return agent.ModelClientResult{}, ctx.Err()
	}}
	manager := persistentTestManager(t, root, model)
	session, err := manager.Start(StartRequest{
		ModelRef: "child", Task: "interrupted task", ParentTurn: 42,
		AllowedTools: []string{"read_file"}, MaxTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	fmt.Println(session.ID)
	<-stop
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}
