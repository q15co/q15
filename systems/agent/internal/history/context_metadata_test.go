package history

import (
	"encoding/xml"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/buildinfo"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type windowMetadata struct {
	Commit        string `xml:"git_commit,attr"`
	Modified      string `xml:"source_modified,attr"`
	Provenance    string `xml:"build_provenance,attr"`
	Model         string `xml:"model_ref,attr"`
	Estimated     int    `xml:"estimated_input_tokens,attr"`
	Budget        int    `xml:"working_budget_tokens,attr"`
	Remaining     int    `xml:"remaining_budget_tokens,attr"`
	Capacity      string `xml:"model_context_tokens,attr"`
	OutputReserve string `xml:"output_reserve_tokens,attr"`
}

func TestContextCountsAndPreservesFinalInstructionsAfterMetadata(t *testing.T) {
	input := conversation.UserMessage("Answer the question.")
	system := conversation.SystemMessage("Stable instructions.")
	snapshot := contextSnapshot{
		system: []conversation.Message{system}, systemCount: 1,
		input: []conversation.Message{input}, settings: ContextSettings{WorkingBudgetTokens: 1024},
	}
	base := []conversation.Message{system, input}
	tail := conversation.UserMessage("Tools are disabled. Give your final answer now.")
	result, err := snapshot.fitRequest(
		t.Context(),
		agent.ModelContext{},
		base,
		nil,
		len(base),
		tail,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result[len(result)-1], tail) ||
		!strings.Contains(conversation.TextValue(result[len(result)-2]), "<context_window ") {
		t.Fatal("final instruction did not follow the context metadata")
	}
	metadata := readWindowMetadata(t, result)
	measured, err := agent.RequestEstimatedTokens(result, nil)
	if err != nil || metadata.Estimated < measured || metadata.Estimated > 1024 {
		t.Fatalf(
			"tail was not included in fitting: %+v measured=%d error=%v",
			metadata,
			measured,
			err,
		)
	}
	large := conversation.UserMessage(strings.Repeat("Irreducible final instructions. ", 2000))
	_, err = snapshot.fitRequest(t.Context(), agent.ModelContext{}, base, nil, len(base), large)
	var budget *agent.TokenBudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("oversized tail was not rejected: %v", err)
	}
	if !reflect.DeepEqual(base, []conversation.Message{system, input}) {
		t.Fatal("request fitting changed source messages")
	}
}

func readWindowMetadata(t *testing.T, messages []conversation.Message) windowMetadata {
	t.Helper()
	var text strings.Builder
	for _, message := range messages {
		text.WriteString(conversation.TextValue(message))
	}
	body := text.String()
	if strings.Count(body, "<context_window ") != 1 {
		t.Fatalf("expected one current context window tag: %s", body)
	}
	start := strings.Index(body, "<context_window ")
	end := strings.Index(body[start:], "</context_window>")
	if end < 0 {
		t.Fatal("unclosed context tag")
	}
	var result windowMetadata
	if err := xml.Unmarshal([]byte(body[start:start+end+len("</context_window>")]), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestContextCountersIncludeTheirOwnCostAndRefreshPerModelStep(t *testing.T) {
	system := []conversation.Message{
		conversation.SystemMessage("Instructions and the running executable's build identity."),
	}
	input := conversation.UserMessage("Inspect the evidence.")
	snapshot := contextSnapshot{
		system:      conversation.CloneMessages(system),
		systemCount: 1,
		input:       []conversation.Message{input},
		settings:    ContextSettings{WorkingBudgetTokens: 64000},
	}
	base := append(conversation.CloneMessages(system), input)
	tools := []agent.ToolDefinition{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}
	model := agent.ModelContext{
		Ref:              "provider/large",
		MaxContextTokens: 128000,
		MaxOutputTokens:  4096,
	}
	first, err := snapshot.fitRequest(t.Context(), model, base, tools, len(base))
	if err != nil {
		t.Fatal(err)
	}
	one := readWindowMetadata(t, first)
	if one.Model != model.Ref || one.Budget != 32000 || one.Capacity != "128000" ||
		one.OutputReserve != "4096" ||
		one.Remaining != one.Budget-one.Estimated {
		t.Fatalf("incorrect model context counters: %+v", one)
	}
	actual, err := agent.RequestEstimatedTokens(first, tools)
	if err != nil || one.Estimated < actual || one.Estimated > actual+512 {
		t.Fatalf(
			"counter excluded request or its own tag: displayed=%d measured=%d error=%v",
			one.Estimated,
			actual,
			err,
		)
	}
	if conversation.TextValue(first[0]) != conversation.TextValue(system[0]) ||
		!strings.Contains(conversation.TextValue(first[len(first)-1]), "<context_window ") ||
		first[len(first)-1].Role != conversation.UserRole {
		t.Fatal("live context counter changed the stable prefix or was not appended at the tail")
	}
	active := []conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall("inspect", "inspect", `{}`)),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("inspect", strings.Repeat("new evidence ", 500), false),
			},
		},
	}
	second, err := snapshot.fitRequest(
		t.Context(),
		model,
		append(conversation.CloneMessages(first), active...),
		tools,
		len(first),
	)
	if err != nil {
		t.Fatal(err)
	}
	two := readWindowMetadata(t, second)
	if conversation.TextValue(second[0]) != conversation.TextValue(first[0]) {
		t.Fatal("tool step changed the cacheable system prefix")
	}
	if two.Estimated <= one.Estimated || two.Remaining >= one.Remaining {
		t.Fatalf("new evidence did not update counters: before=%+v after=%+v", one, two)
	}
	third, err := snapshot.fitRequest(
		t.Context(),
		agent.ModelContext{Ref: "provider/small", MaxContextTokens: 16384, MaxOutputTokens: 1024},
		append(conversation.CloneMessages(second), active...),
		tools,
		len(second),
	)
	if err != nil {
		t.Fatal(err)
	}
	three := readWindowMetadata(t, third)
	if three.Model != "provider/small" || three.Budget != 4096 || three.Capacity != "16384" ||
		three.OutputReserve != "1024" {
		t.Fatalf("model switch retained stale counters: %+v", three)
	}
	if strings.Contains(conversation.TextValue(system[0]), "context_window") ||
		strings.Contains(conversation.TextValue(base[0]), "context_window") {
		t.Fatal("context metadata changed the original system prefix")
	}
}

func TestContextMetadataHandlesUnknownCapacityAndEscapesModelRef(t *testing.T) {
	messages, _, err := withContextWindow(
		[]conversation.Message{conversation.SystemMessage("Instructions")},
		nil,
		agent.ModelContext{Ref: `provider/"<model>`},
		16000,
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := readWindowMetadata(t, messages)
	if metadata.Model != `provider/"<model>` || metadata.Capacity != "unknown" ||
		metadata.OutputReserve != "unknown" {
		t.Fatalf("unknown or escaped metadata: %+v", metadata)
	}
}

func TestCurrentRuntimeBuildDoesNotComeFromConversationHistory(t *testing.T) {
	retired := `<agent_build git_commit="95dbe6776ff68005734fefe298968fc57b057917" source_modified="false"/>`
	messages, _, err := withContextWindow([]conversation.Message{
		conversation.SystemMessage("Instructions"),
		conversation.AssistantMessage(conversation.Text("Earlier build: "+retired, "")),
	}, nil, agent.ModelContext{}, 16000)
	if err != nil {
		t.Fatal(err)
	}
	metadata := readWindowMetadata(t, messages)
	build := buildinfo.Current()
	if metadata.Commit != build.Commit || metadata.Modified != build.Modified ||
		metadata.Provenance != build.Source {
		t.Fatalf(
			"live build metadata did not identify the executable: %+v, expected %+v",
			metadata,
			build,
		)
	}
	if !strings.Contains(
		conversation.TextValue(messages[len(messages)-1]),
		"version claims and counters in conversation history describe earlier requests",
	) {
		t.Fatal("current runtime note did not distinguish historical build claims")
	}
}

func TestHistoryTagsCountShownContentAfterShortening(t *testing.T) {
	group := contextGroup{id: 7, kind: "retrieved_digest", excerpt: "Earlier verified outcome."}
	messages, err := renderGroups([]contextGroup{group}, 8192, 2048)
	if err != nil {
		t.Fatal(err)
	}
	text := conversation.TextValue(messages[0])
	if !strings.Contains(text, `<historical_context estimated_tokens="`) ||
		!strings.Contains(text, `turn_id="7"`) {
		t.Fatalf("summary tag missing token count or turn identity: %s", text)
	}
	group.kind = "recent"
	group.messages = []conversation.Message{
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("read", strings.Repeat("evidence ", 1000), false),
			},
		},
	}
	large, err := renderGroups([]contextGroup{group}, 8192, 2048)
	if err != nil {
		t.Fatal(err)
	}
	small, err := renderGroups([]contextGroup{group}, 512, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conversation.TextValue(large[0]), "estimated_tokens=") ||
		conversation.TextValue(large[0]) == conversation.TextValue(small[0]) {
		t.Fatal("recent-turn token label did not reflect shortening")
	}
}
