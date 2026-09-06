package telegram

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestCommandProgressPreviewBoundaries(t *testing.T) {
	for _, mode := range []struct {
		name  string
		value progressMode
		limit int
	}{
		{name: "progress", value: progressModeProgress, limit: 56},
		{name: "verbose", value: progressModeVerbose, limit: 96},
	} {
		for _, size := range []struct {
			name   string
			offset int
		}{
			{name: "below limit", offset: -1},
			{name: "at limit"},
			{name: "above limit", offset: 1},
		} {
			t.Run(mode.name+"/"+size.name, func(t *testing.T) {
				command := "go test " + strings.Repeat("界", mode.limit+size.offset-8)
				call := agent.ToolCall{
					Name:      "exec",
					Arguments: progressTestArgs(t, "command", command),
				}
				got := summarizeToolCall(call, mode.value)
				wantDetail := command
				if size.offset > 0 {
					wantDetail = "go test " + strings.Repeat("界", mode.limit-11) + "..."
				}
				want := "💻 Running `" + wantDetail + "`"
				if got != want {
					t.Fatalf("summary = %q, want %q", got, want)
				}
				if !utf8.ValidString(got) {
					t.Fatalf("summary is not valid UTF-8: %q", got)
				}
			})
		}
	}
}

func TestProgressPreviewsRemoveControlCharacters(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  string
	}{
		{name: "exec", key: "command"},
		{name: "exec_read", key: "session_id"},
		{name: "exec_write", key: "session_id"},
		{name: "exec_kill", key: "session_id"},
		{name: "read_file", key: "path"},
		{name: "write_file", key: "path"},
		{name: "edit_file", key: "path"},
		{name: "web_search", key: "query"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := "visible\x00\n\t\x1b\u0085\u202e\u2066`内容`\u2069 tail"
			got := summarizeToolCall(agent.ToolCall{
				Name:      tt.name,
				Arguments: progressTestArgs(t, tt.key, detail),
			}, progressModeProgress)
			if !strings.Contains(got, "`visible '内容' tail`") {
				t.Fatalf("summary lost useful text or code boundary: %q", got)
			}
			for _, r := range got {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Fatalf("summary contains control character %U: %q", r, got)
				}
			}
		})
	}
}

func TestFileProgressPreservesLongPathFilename(t *testing.T) {
	for _, tt := range []struct {
		name string
		verb string
	}{
		{name: "read_file", verb: "📖 Reading"},
		{name: "write_file", verb: "✍️ Writing"},
		{name: "edit_file", verb: "🛠️ Editing"},
	} {
		for _, mode := range []struct {
			name  string
			value progressMode
			limit int
		}{
			{name: "progress", value: progressModeProgress, limit: 56},
			{name: "verbose", value: progressModeVerbose, limit: 96},
		} {
			t.Run(tt.name+"/"+mode.name, func(t *testing.T) {
				path := "/workspace/" + strings.Repeat("目录/", 100) + "配置.yaml"
				got := summarizeToolCall(agent.ToolCall{
					Name:      tt.name,
					Arguments: progressTestArgs(t, "path", path),
				}, mode.value)
				prefix := tt.verb + " `"
				if !strings.HasPrefix(got, prefix+"...") || !strings.HasSuffix(got, "/配置.yaml`") {
					t.Fatalf("summary lost action, truncation marker, or filename: %q", got)
				}
				detail := strings.TrimSuffix(strings.TrimPrefix(got, prefix), "`")
				if !utf8.ValidString(detail) || utf8.RuneCountInString(detail) > mode.limit {
					t.Fatalf("invalid or oversized path preview: %q", detail)
				}
			})
		}
	}
}

func TestProgressMissingAndMalformedArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "exec", want: "💻 Running command"},
		{name: "exec_read", want: "💻 Checking command"},
		{name: "exec_write", want: "💻 Sending command input"},
		{name: "exec_kill", want: "💻 Stopping command"},
		{name: "read_file", want: "📖 Reading file"},
		{name: "write_file", want: "✍️ Writing file"},
		{name: "edit_file", want: "🛠️ Editing file"},
		{name: "web_fetch", want: "🌐 Fetching webpage"},
		{name: "web_search", want: "🌐 Searching the web"},
	} {
		for _, args := range []struct {
			name  string
			value string
		}{
			{name: "absent"},
			{name: "missing fields", value: `{}`},
			{name: "malformed", value: `{"command":`},
			{name: "null", value: `null`},
			{name: "array", value: `[]`},
			{name: "scalar", value: `42`},
			{name: "wrong types", value: `{"command":7,"session_id":{},"path":[],"url":true,"query":null}`},
			{name: "blank", value: `{"command":" ","session_id":" ","path":" ","url":" ","query":" "}`},
		} {
			t.Run(tt.name+"/"+args.name, func(t *testing.T) {
				got := summarizeToolCall(
					agent.ToolCall{Name: tt.name, Arguments: args.value},
					progressModeProgress,
				)
				if got != tt.want {
					t.Fatalf("summary = %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestToolFinishedSummariesStayConcise(t *testing.T) {
	largeError := errors.New(strings.Repeat("command output\n", 10000))
	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "exec", want: "⚠️ Command step failed"},
		{name: "exec_read", want: "⚠️ Command step failed"},
		{name: "exec_write", want: "⚠️ Command step failed"},
		{name: "exec_kill", want: "⚠️ Command step failed"},
		{name: "read_file", want: "⚠️ File step failed"},
		{name: "write_file", want: "⚠️ File step failed"},
		{name: "edit_file", want: "⚠️ File step failed"},
		{name: "apply_patch", want: "⚠️ File step failed"},
		{name: "web_fetch", want: "⚠️ Tool step failed"},
		{name: "web_search", want: "⚠️ Tool step failed"},
		{name: "custom_tool", want: "⚠️ Tool step failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			call := agent.ToolCall{Name: tt.name}
			if got := summarizeToolFinished(call, largeError); got != tt.want {
				t.Fatalf("failure summary = %q, want %q", got, tt.want)
			}
			if got := summarizeToolFinished(call, nil); got != "🧠 Reviewing result…" {
				t.Fatalf("success summary prematurely claims completion: %q", got)
			}
		})
	}
}

func progressTestArgs(t *testing.T, key, value string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
