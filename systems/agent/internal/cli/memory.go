package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/history"
)

func runMemory(ctx context.Context, env command.Env, args []string) error {
	if len(args) == 0 || asksForHelp(args[0]) {
		writeMemoryUsage(env.Stdout)
		return nil
	}
	switch args[0] {
	case "import", "status", "backfill":
	default:
		return command.Usagef("unknown memory command %q", args[0])
	}
	var f historyFlags
	fs := flag.NewFlagSet("memory "+args[0], flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.StringVar(&f.agent, "agent", "Q15", "configured agent name")
	fs.StringVar(
		&f.databaseEnv,
		"database-env",
		"Q15_DATABASE_URL",
		"database connection environment variable (or its _FILE secret)",
	)
	fs.StringVar(&f.memoryRoot, "memory-root", "", "frozen legacy memory root, for import only")
	fs.StringVar(
		&f.configPath,
		"config",
		"/etc/q15/agent/config.yaml",
		"agent configuration, for backfill",
	)
	fs.BoolVar(&f.verify, "verify", false, "verify every imported turn without writing")
	fs.BoolVar(&f.prepare, "prepare", false, "record cutover readiness after complete backfill")
	fs.IntVar(&f.batches, "batches", 0, "maximum backfill batches; zero runs until idle")
	if err := fs.Parse(args[1:]); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() != 0 {
		return command.Usagef("memory %s takes no positional arguments", args[0])
	}
	if f.agent == "" || f.batches < 0 {
		return command.Usagef("agent must be nonempty and batches must be nonnegative")
	}
	if args[0] == "import" && f.memoryRoot == "" {
		return command.Usagef("memory import requires --memory-root pointing to a frozen snapshot")
	}
	if (f.verify && args[0] != "import") || (f.prepare && args[0] != "backfill") ||
		(f.memoryRoot != "" && args[0] != "import") {
		return command.Usagef(
			"verify and memory-root belong to import; prepare belongs to backfill",
		)
	}
	dsn, err := config.ResolveDatabaseURL(f.databaseEnv)
	if err != nil {
		return err
	}
	if args[0] == "backfill" {
		return runHistoryBackfill(ctx, env, f, dsn)
	}
	var store *history.Store
	if args[0] == "status" || f.verify {
		store, err = history.OpenReadOnly(ctx, dsn, f.agent)
	} else {
		store, err = history.Open(ctx, dsn, f.agent)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	if args[0] == "import" {
		report, err := store.Import(ctx, f.memoryRoot, f.verify)
		if writeErr := json.NewEncoder(env.Stdout).Encode(report); writeErr != nil {
			return writeErr
		}
		return err
	}
	status, err := store.Coverage(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(env.Stdout).Encode(status)
}

type historyFlags struct {
	agent, databaseEnv, memoryRoot, configPath string
	verify, prepare                            bool
	batches                                    int
}

func runHistoryBackfill(ctx context.Context, env command.Env, f historyFlags, dsn string) error {
	cfg, err := config.Load(f.configPath)
	if err != nil {
		return err
	}
	if cfg.Agent == nil || cfg.Agent.Name != f.agent {
		return command.Usagef("--agent must match the agent in --config")
	}
	settings, err := cfg.ResolveHistory()
	if err != nil {
		return err
	}
	store, err := history.Open(ctx, dsn, f.agent)
	if err != nil {
		return err
	}
	defer store.Close()
	embedder, err := embed.NewEmbedder(
		ctx,
		embed.Settings{
			Provider:   settings.EmbeddingProvider,
			BaseURL:    settings.EmbeddingBaseURL,
			APIKey:     settings.EmbeddingAPIKey,
			Model:      settings.EmbeddingModel,
			Dimensions: settings.EmbeddingDimensions,
			BatchSize:  settings.EmbeddingBatchSize,
		},
	)
	if err != nil {
		return err
	}
	digester, err := history.NewOllamaDigester(
		settings.SummaryBaseURL,
		settings.SummaryAPIKey,
		settings.SummaryProvider,
		settings.SummaryModel,
	)
	if err != nil {
		return err
	}
	indexer, err := history.NewIndexer(
		ctx,
		store,
		history.EmbeddingSpace{
			Provider:   settings.EmbeddingProvider,
			Model:      settings.EmbeddingModel,
			Dimensions: settings.EmbeddingDimensions,
		},
		embedder,
		digester,
	)
	if err != nil {
		return err
	}
	for batch := 0; f.batches == 0 || batch < f.batches; batch++ {
		batchCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		n, batchErr := indexer.RunBatch(batchCtx, 8)
		cancel()
		status, err := indexer.Status(ctx)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(env.Stdout).Encode(status); err != nil {
			return err
		}
		if batchErr != nil {
			return fmt.Errorf(
				"history backfill batch failed; completed work is saved, rerun to resume",
			)
		}
		if n == 0 {
			break
		}
	}
	if f.prepare {
		return indexer.PrepareCutover(ctx)
	}
	return nil
}

func writeMemoryUsage(w io.Writer) {
	fmt.Fprint(w, `Postgres conversation history and resumable cutover preparation.

usage:
  q15 memory import --agent NAME --memory-root FROZEN_PATH [--verify]
  q15 memory backfill --agent NAME --config PATH [--batches N] [--prepare]
  q15 memory status --agent NAME

Connection: Q15_DATABASE_URL or Q15_DATABASE_URL_FILE; override its name with --database-env.
Import is idempotent and verifies every turn. --verify and status are read-only.
Backfill uses the explicitly configured Ollama summary model and embedding space.
Stop the old agent before the final snapshot. Prepare must succeed before the new runtime starts.
`)
}
