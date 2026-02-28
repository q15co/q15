package agent

import "context"

// Agent defines the minimal behavior the app needs from an agent runtime.
type Agent interface {
	Reply(ctx context.Context, userInput string) (string, error)
}
