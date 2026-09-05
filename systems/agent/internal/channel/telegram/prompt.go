package telegram

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
)

// ResponseFormatPrompt describes the safe Markdown surface available to an
// agent whose final response will be delivered through Telegram. Keeping the
// guidance with the adapter makes the model-facing contract track the
// renderer instead of becoming stale global prompt text.
func ResponseFormatPrompt() string {
	body := strings.Join([]string{
		"## Telegram rich Markdown",
		"Write final user-facing text as concise Markdown supported by Telegram rich messages. Use structure and emphasis when they improve the answer; do not demonstrate every feature unless asked.",
		"",
		"Supported block syntax:",
		"- Headings (`#` through `######`).",
		"- Ordered lists (`1.`), unordered lists (`-`), and task lists (`- [ ]` / `- [x]`).",
		"- Block quotations (`>`), dividers (`---`), and fenced code blocks with an optional language tag.",
		"- Markdown tables. A separator cell needs at least one hyphen, so compact tables such as `|-|-|` are valid.",
		"",
		"Supported inline syntax:",
		"- Bold (`**text**`), italic (`*text*`), strikethrough (`~~text~~`), inline code, spoilers (`||text||`), and highlights (`==text==`).",
		"- Inline formulas (`$x^2$`), block formulas (`$$x^2$$`), footnote references (`[^1]`) with definitions (`[^1]: text`), and ordinary Markdown links.",
		"",
		"Safety and delivery constraints:",
		"- Use only `http`, `https`, `mailto`, `tel`, or fragment link targets. Never emit `tg://` or executable link schemes.",
		"- Do not emit raw HTML outside code, Markdown images, buttons, callbacks, Web Apps, login links, maps, or embedded media. Use the available media tools for attachments.",
	}, "\n")
	return agent.RenderPromptElement(
		"response_format",
		map[string]string{"channel": bus.ChannelTelegram},
		body,
	)
}
