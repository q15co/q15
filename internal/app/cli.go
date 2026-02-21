package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"q15.co/sandbox/internal/agent"
	"q15.co/sandbox/internal/provider/moonshot"
	"q15.co/sandbox/internal/tools"
)

func RunCLI(ctx context.Context, model string) error {
	modelAdapter := moonshot.NewClient()
	toolRunner := tools.NewShell()
	var a agent.Agent = agent.NewLoop(modelAdapter, toolRunner, model, agent.DefaultSystemPrompt)

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Type your question and press enter.")
	fmt.Println("Use '/reset' to clear chat history. Type 'exit' to quit.")

	for {
		fmt.Print("#> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("input error: %w", err)
			}
			return nil
		}

		question := strings.TrimSpace(scanner.Text())
		if question == "" {
			continue
		}
		if question == "exit" || question == "quit" {
			return nil
		}
		if question == "/reset" {
			if err := a.Reset(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "reset error: %v\n", err)
				continue
			}
			fmt.Println("history reset")
			continue
		}

		answer, err := a.Reply(ctx, question)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reply error: %v\n", err)
			continue
		}
		fmt.Printf("AI> %s\n", answer)
	}
}
