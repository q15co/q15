package memory

type EventKind string

const (
	UserMessageKind EventKind = "user"
)

type Event struct {
	Kind    EventKind
	Content string
}
