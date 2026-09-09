package telegram

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestCommandProgressPreviewBoundaries(t *testing.T) {
	for _, mode := range []struct {
		name  string
		value progressMode
		limit int
	}{
		{name: "progress", value: progressModeProgress, limit: 320},
		{name: "verbose", value: progressModeVerbose, limit: 640},
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
				want := "💻 Running command\n\n```bash\n" + wantDetail + "\n```"
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

func TestCommandProgressPreviewLineBoundaries(t *testing.T) {
	for _, mode := range []struct {
		name  string
		value progressMode
		lines int
	}{
		{name: "progress", value: progressModeProgress, lines: 5},
		{name: "verbose", value: progressModeVerbose, lines: 10},
	} {
		for _, size := range []struct {
			name   string
			offset int
		}{
			{name: "below limit", offset: -1},
			{name: "at limit"},
			{name: "above limit", offset: 1},
			{name: "huge command", offset: 10_000},
		} {
			t.Run(mode.name+"/"+size.name, func(t *testing.T) {
				command := strings.Repeat(
					"printf 'ok'\n",
					mode.lines+size.offset-1,
				) + "printf 'end'"
				got := commandProgressPreview(command, mode.value)
				want := command
				if size.offset > 0 {
					want = strings.Repeat("printf 'ok'\n", mode.lines-1) + "printf 'ok'..."
				}
				if got != want {
					t.Fatalf("preview = %q, want %q", got, want)
				}
				if lines := strings.Count(got, "\n") + 1; lines > mode.lines {
					t.Fatalf("preview has %d lines, limit %d: %q", lines, mode.lines, got)
				}
			})
		}
	}
}

func TestCommandProgressPreservesShellSyntaxAndRendering(t *testing.T) {
	for _, command := range []string{
		"printf '%s\\n' `pwd`\n\tprintf '%s\\n' \"$PATH\" > /tmp/out",
		"cat <<'EOF'\n```\n<tg-button> & **literal** `ticks`\n````\nEOF",
		"cat <<'EOF'\n| A | B |\n|-|-|\n| 1 | 2 |\nEOF",
		strings.Repeat("`", 640),
	} {
		got := summarizeToolCall(agent.ToolCall{
			Name:      "exec",
			Arguments: progressTestArgs(t, "command", command),
		}, progressModeVerbose)
		chunks := planRichText(got)
		if len(chunks) != 1 || !chunks[0].rich || chunks[0].richMarkdown != got {
			t.Fatalf("rich rendering altered the command preview: %#v", chunks)
		}
		blocks := parseMarkdownBlocks(got)
		if len(blocks) != 2 {
			t.Fatalf("preview escaped the single code block: %#v", blocks)
		}
		wantHTML := "💻 Running command\n\n<pre><code class=\"language-bash\">" +
			escapeHTML(command) + "\n</code></pre>"
		if html := markdownToTelegramHTML(chunks[0].fallback); html != wantHTML {
			t.Fatalf("fallback altered the shell syntax or escaping: %q, want %q", html, wantHTML)
		}
	}
}

func TestCommandProgressEditRendersRichAndHTMLFallback(t *testing.T) {
	caller := &mockAPICaller{responses: []*ta.Response{
		telegramAPIError(400, "rich edit rejected"),
		{Ok: true, Result: []byte(`{}`)},
	}}
	ch := newTestChannelWithCaller(t, caller)
	command := "cat <<'EOF'\n```\n<tg-button> & **literal** `ticks`\nEOF"
	status := summarizeToolCall(agent.ToolCall{
		Name:      "exec",
		Arguments: progressTestArgs(t, "command", command),
	}, progressModeProgress)
	if err := ch.EditText(t.Context(), "123", "456", status); err != nil {
		t.Fatalf("EditText() error = %v", err)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want rich attempt plus HTML fallback", len(caller.calls))
	}
	for _, call := range caller.calls {
		if !strings.HasSuffix(call.url, "/editMessageText") {
			t.Fatalf("URL = %q, want /editMessageText", call.url)
		}
	}
	if got := richMessageBody(t, caller.calls[0])["markdown"]; got != status {
		t.Fatalf("rich Markdown = %#v, want %q", got, status)
	}
	want := "💻 Running command\n\n<pre><code class=\"language-bash\">" +
		"cat &lt;&lt;'EOF'\n```\n&lt;tg-button&gt; &amp; **literal** `ticks`\nEOF\n</code></pre>"
	if got := caller.calls[1].body["text"]; got != want {
		t.Fatalf("HTML fallback = %#v, want %q", got, want)
	}
	if got := caller.calls[1].body["parse_mode"]; got != "HTML" {
		t.Fatalf("fallback parse mode = %#v, want HTML", got)
	}
}

func TestCommandProgressControlsPreserveUsefulWhitespace(t *testing.T) {
	command := "printf 'a  b'\r\n\tprintf `pwd`\x00\x1b\u0085\u202e\u2066\u2069\rprintf 'end'"
	got := commandProgressPreview(command, progressModeProgress)
	want := "printf 'a  b'\n\tprintf `pwd`      \nprintf 'end'"
	if got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
	for _, r := range got {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Cf, r) {
			t.Fatalf("preview contains control character %U: %q", r, got)
		}
	}
	separators := "one\u2028two\u2029three\nfour\nfive\nsix"
	if got := commandProgressPreview(separators, progressModeProgress); got != "one\ntwo\nthree\nfour\nfive..." {
		t.Fatalf("Unicode line separators escaped line limit: %q", got)
	}
	if got := summarizeToolCall(agent.ToolCall{
		Name:      "exec",
		Arguments: progressTestArgs(t, "command", "\x00\x1b\u202e"),
	}, progressModeProgress); got != "💻 Running command" {
		t.Fatalf("control-only command = %q, want fallback", got)
	}
}

func TestProgressPreviewsRemoveControlCharacters(t *testing.T) {
	for _, tt := range []struct {
		name string
		key  string
	}{
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
