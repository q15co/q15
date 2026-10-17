package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestSemanticConversionPreservesExistingOpenLoops(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, after string
		wantError   bool
	}{
		{name: "removed", wantError: true},
		{name: "rewritten intention", after: strings.Replace(validConversionLoop, "Ship the release.", "Abandon the release.", 1), wantError: true},
		{name: "rewritten trigger", after: strings.Replace(validConversionLoop, "next-session", "date:2026-10-16", 1), wantError: true},
		{name: "rewritten owner", after: strings.Replace(validConversionLoop, "owner: agent", "owner: user", 1), wantError: true},
		{name: "rewritten source", after: strings.Replace(validConversionLoop, "src: t12", "src: t13", 1), wantError: true},
		{name: "replaced ID", after: strings.Replace(validConversionLoop, "l-2026-0001", "l-2026-0002", 1), wantError: true},
		{name: "unchanged", after: validConversionLoop},
		{name: "new intention added", after: validConversionLoop + strings.Replace(validConversionLoop, "l-2026-0001", "l-2026-0002", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			baseline := emptyConversionBaseline()
			baseline[openLoopsRuntimePath] = validConversionLoop
			loader := &semanticMemoryJobLoader{openLoops: test.after}
			_, err := applyConversion(t, loader, baseline, openLoopsRuntimePath)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "existing open loop") {
					t.Fatalf("ApplyResult error=%v; want the existing loop preserved", err)
				}
			} else if err != nil {
				t.Fatalf("ApplyResult rejected preserved loops: %v", err)
			}
		})
	}
}

func TestSemanticConversionChecksBytesAfterFailedFileTool(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, path, before, after, wantError string
	}{
		{name: "root invalid metadata", path: semanticFactsRelativePath, before: validConversionClaim,
			after: strings.Replace(validConversionClaim, "kind: fact", "kind: unknown", 1), wantError: "invalid written semantic claim"},
		{name: "root lost provenance", path: semanticFactsRelativePath, before: validConversionClaim,
			after: strings.Replace(validConversionClaim, "src: t12", "src: t13", 1), wantError: "removed existing provenance"},
		{name: "root over cap", path: semanticFactsRelativePath, before: "# Facts\n",
			after: strings.Repeat("x", semanticRootCapBytes+1), wantError: "over cap"},
		{name: "created topic invalid claim", path: "semantic/topics/new.md",
			after: "- An untyped durable detail.\n", wantError: "invalid written semantic claim"},
		{name: "created topic over cap", path: "semantic/topics/new.md",
			after: strings.Repeat("x", semanticTopicCapBytes+1), wantError: "over cap"},
		{name: "unchanged over cap", path: semanticFactsRelativePath,
			before: strings.Repeat("x", semanticRootCapBytes+1), after: strings.Repeat("x", semanticRootCapBytes+1)},
		{name: "valid changed bytes", path: semanticFactsRelativePath, before: validConversionClaim,
			after: strings.Replace(validConversionClaim, "target is stable", "target remains stable", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			baseline := emptyConversionBaseline()
			if test.before != "" {
				baseline[memoryRuntimePrefix+test.path] = test.before
			}
			loader := &conversionValidationLoader{files: map[string]string{
				workingMemoryRelativePath: "", "working/LEDGER.md": "", test.path: test.after,
			}}
			messages := append(
				conversionRunMessages(),
				conversation.AssistantMessage(conversation.ToolCall(
					"failed-patch",
					"apply_patch",
					fmt.Sprintf(
						`{"patch":%q}`,
						"*** Begin Patch\n*** Update File: /memory/"+test.path+"\n@@\n-old\n+new\n*** End Patch",
					),
				)),
				conversation.ToolResultMessage(
					"failed-patch",
					"later write failed; restoration failed",
					true,
				),
			)
			_, err := NewSemanticMemoryExtractionRegistration().NewJob().
				ApplyResult(context.Background(), loader, JobOutput{
					Spec: Spec{Baseline: baseline}, Messages: messages,
				})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf(
						"ApplyResult error=%v; want %q despite the failed tool result",
						err,
						test.wantError,
					)
				}
			} else if err != nil {
				t.Fatalf("ApplyResult rejected valid resulting bytes: %v", err)
			}
		})
	}
}
