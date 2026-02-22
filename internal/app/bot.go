package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"q15.co/sandbox/internal/agent"
	"q15.co/sandbox/internal/bus"
	"q15.co/sandbox/internal/channel/telegram"
	"q15.co/sandbox/internal/config"
	"q15.co/sandbox/internal/provider/moonshot"
	"q15.co/sandbox/internal/sandbox"
	"q15.co/sandbox/internal/tools"
)

func runBot(ctx context.Context, rt config.AgentRuntime) error {
	token := strings.TrimSpace(rt.TelegramToken)
	if token == "" {
		return errors.New("telegram token is required")
	}
	apiKey := strings.TrimSpace(rt.ProviderAPIKey)
	if apiKey == "" {
		return errors.New("provider api key is required")
	}

	models := normalizeModelList(rt.Models)
	if len(models) == 0 {
		return errors.New("at least one model is required")
	}

	modelAdapter, err := newModelAdapter(rt)
	if err != nil {
		return err
	}

	agentSandbox := sandbox.New(sandbox.Settings{
		ContainerName:    rt.SandboxContainerName,
		FromImage:        rt.SandboxFromImage,
		WorkspaceHostDir: rt.WorkspaceHostDir,
		WorkspaceDir:     rt.WorkspaceDir,
		Network:          rt.SandboxNetwork,
	})
	if sandbox.VerboseEnabled() {
		fmt.Printf("[app] preparing sandbox for agent=%q container=%q workspace_host_dir=%q workspace_dir=%q from_image=%q network=%q\n",
			rt.Name, rt.SandboxContainerName, rt.WorkspaceHostDir, rt.WorkspaceDir, rt.SandboxFromImage, rt.SandboxNetwork)
	}
	if err := agentSandbox.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare sandbox for agent %q: %w", rt.Name, err)
	}
	if sandbox.VerboseEnabled() {
		fmt.Printf("[app] sandbox ready for agent=%q container=%q\n", rt.Name, rt.SandboxContainerName)
	}

	toolRunner := tools.NewShell(agentSandbox)
	messageBus := bus.New(bus.DefaultBufferSize)

	var (
		mu     sync.Mutex
		agents = make(map[string]agent.Agent)
	)

	getAgent := func(sessionKey string) agent.Agent {
		mu.Lock()
		defer mu.Unlock()

		if a, ok := agents[sessionKey]; ok {
			return a
		}

		a := agent.NewLoop(modelAdapter, toolRunner, models, agent.DefaultSystemPrompt)
		agents[sessionKey] = a
		return a
	}

	channel, err := telegram.NewChannel(token, func(msg telegram.IncomingMessage) {
		err := messageBus.PublishInbound(ctx, bus.InboundMessage{
			Channel: bus.ChannelTelegram,
			ChatID:  msg.ChatID,
			UserID:  msg.UserID,
			Text:    msg.Text,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "publish inbound error: %v\n", err)
		}
	}, telegram.WithAllowedUserIDs(rt.TelegramAllowedUserIDs))
	if err != nil {
		return err
	}
	if err := channel.Start(ctx); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- runAgentWorker(ctx, messageBus, getAgent)
	}()
	go func() {
		errCh <- runOutboundWorker(ctx, messageBus, bus.ChannelTelegram, channel.SendText)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func newModelAdapter(rt config.AgentRuntime) (agent.Model, error) {
	switch strings.ToLower(strings.TrimSpace(rt.ProviderType)) {
	case "openai-compatible":
		return moonshot.NewClient(rt.ProviderBaseURL, rt.ProviderAPIKey), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", rt.ProviderType)
	}
}

func normalizeModelList(models []string) []string {
	if len(models) == 0 {
		return nil
	}

	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
