package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"assistant/internal/chat"
	"assistant/internal/config"
	"assistant/internal/db"
	"assistant/internal/llm"
	"assistant/internal/llm/demo"
	"assistant/internal/llm/ollama"
	"assistant/internal/llm/vertex"
	"assistant/internal/llmcalls"
	"assistant/internal/metrics"
	"assistant/internal/models"
	"assistant/internal/server"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
	"assistant/internal/tools"
	"assistant/internal/users"
	"assistant/internal/workflow"
	"assistant/internal/workflows"
)

const (
	httpAddr        = ":8080"
	shutdownTimeout = 10 * time.Second
	migrateTimeout  = time.Minute
)

// newLogger is the one place logging is configured: JSON lines on stdout.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// loadConfig reads .env (when present, for local runs) and the environment.
func loadConfig() (config.Config, error) {
	if err := config.ApplyDotEnv(".env"); err != nil {
		return config.Config{}, err
	}
	return config.Load(os.LookupEnv)
}

// migrate applies the database schema and exits. It is what the deployment's
// one-time job runs before the server starts.
func migrate() int {
	log := newLogger()
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("create database pool", "error", err)
		return 1
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		log.Error("migrate", "error", err)
		return 1
	}
	log.Info("schema applied")
	return 0
}

// app is the assembled program: the HTTP handler and the queue worker, sharing
// one set of metrics.
type app struct {
	handler http.Handler
	worker  *tasks.Worker
	tasks   *tasks.Store
	log     *slog.Logger
}

// modelSetup is the model configuration and the providers that serve it,
// keyed by provider type. Tests pass scripted providers.
type modelSetup struct {
	config    *models.Config
	providers map[string]llm.Provider
}

// loadModels reads the models file; its error names the setting and the file
// so a misconfigured deployment is fixed at once.
func loadModels(path, vertexTokenFile string) (modelSetup, error) {
	cfg, err := models.Load(path)
	if err != nil {
		return modelSetup{}, fmt.Errorf("%s (%s): %w", config.EnvModelsFile, path, err)
	}
	for _, p := range cfg.Providers {
		if p.Type == "vertex" && vertexTokenFile == "" {
			return modelSetup{}, fmt.Errorf("%s (%s): provider %q is a vertex provider, which needs %s to name the file with its access token", config.EnvModelsFile, path, p.ID, config.EnvVertexTokenFile)
		}
	}
	return modelSetup{config: cfg, providers: map[string]llm.Provider{
		"ollama": ollama.New(), "demo": demo.New(), "vertex": vertex.New(vertexTokenFile),
	}}, nil
}

// newToolRegistry is the tools workflows can call. They are stand-ins until
// memory, the knowledge base and web search exist.
func newToolRegistry() (*workflow.Registry, error) {
	return workflow.NewRegistry(tools.Stubs()...)
}

// loadWorkflows reads the YAML workflows in dir. The directory must exist: a
// missing mount is a deployment mistake that must not look like "no workflows".
// Its error names the setting and lists every problem with file and line.
func loadWorkflows(dir string, reg *workflow.Registry) (map[string]*workflow.Workflow, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s (%s): the directory of workflow files does not exist", config.EnvWorkflowsDir, dir)
	}
	wfs, err := workflows.LoadDir(dir, reg, workflows.BuiltinIDs)
	if err != nil {
		return nil, fmt.Errorf("%s (%s): invalid workflows:\n%w", config.EnvWorkflowsDir, dir, err)
	}
	return wfs, nil
}

// newApp wires the stores, the model router, the workflow engine, the worker
// and the HTTP handler together. The clock is how the noop workflow waits;
// tests pass a fake. wfs are the YAML workflows tasks can name.
func newApp(pool *pgxpool.Pool, log *slog.Logger, clock workflows.Clock, setup modelSetup, wfs map[string]*workflow.Workflow) (*app, error) {
	m := metrics.New()
	taskStore := tasks.NewStore(pool)
	callStore := llmcalls.NewStore(pool)

	recorder := llmcalls.NewRecorder(setup.config, callStore, m, log)
	router, err := llm.NewRouter(setup.config, setup.providers, llm.WithObserver(recorder), llm.RequireAttribution())
	if err != nil {
		return nil, fmt.Errorf("models: %w", err)
	}
	m.SetModelAvailability(func() []metrics.ModelAvailability {
		status := router.Status()
		out := make([]metrics.ModelAvailability, len(status))
		for i, s := range status {
			out[i] = metrics.ModelAvailability{Model: s.ID, Available: s.Available}
		}
		return out
	})

	toolRegistry, err := newToolRegistry()
	if err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	prompts, err := workflow.DefaultPrompts()
	if err != nil {
		return nil, fmt.Errorf("workflow prompts: %w", err)
	}
	stepStore := tasksteps.NewStore(pool)
	engine := &workflow.Engine{
		LLM: router, Tools: toolRegistry, Prompts: prompts,
		Recorder: tasksteps.NewRecorder(stepStore, m, log),
	}
	runner := workflows.NewRunner(workflows.New(clock, workflows.WithLLM(router)), &workflows.EngineRunner{Engine: engine, Workflows: wfs})

	conversations := &chat.Service{
		Store: chat.NewStore(pool), Tasks: taskStore, Catalog: runner,
		Channel: &chat.MockChannel{}, Errors: m, Log: log,
	}
	worker := &tasks.Worker{Queue: taskStore, Runner: runner, Observer: m, Notifier: conversations, Log: log}

	return &app{
		handler: server.NewRouter(server.Deps{
			DB: pool, Users: users.NewStore(pool), Tasks: taskStore, Calls: callStore, Steps: stepStore, Worker: worker, Workflows: runner, Chat: conversations, Models: router, Metrics: m, Log: log,
		}),
		worker: worker,
		tasks:  taskStore,
		log:    log,
	}, nil
}

// recoverCrashed fails the tasks a previous process left running. It must run
// before the worker starts.
func (a *app) recoverCrashed(ctx context.Context) error {
	n, err := a.tasks.RecoverCrashed(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		a.log.Warn("failed tasks left running by a previous process", "count", n)
	}
	return nil
}

func serve() int {
	log := newLogger()
	slog.SetDefault(log)

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("create database pool", "error", err)
		return 1
	}
	defer pool.Close()

	setup, err := loadModels(cfg.ModelsFile, cfg.VertexTokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 1
	}
	toolRegistry, err := newToolRegistry()
	if err != nil {
		log.Error("startup", "error", err)
		return 1
	}
	wfs, err := loadWorkflows(cfg.WorkflowsDir, toolRegistry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 1
	}
	log.Info("workflows loaded", "count", len(wfs), "dir", cfg.WorkflowsDir)
	a, err := newApp(pool, log, workflows.RealClock{}, setup, wfs)
	if err != nil {
		log.Error("startup", "error", err)
		return 1
	}
	if err := a.recoverCrashed(ctx); err != nil {
		log.Error("startup", "error", err)
		return 1
	}

	var workerDone sync.WaitGroup
	workerDone.Add(1)
	go func() {
		defer workerDone.Done()
		a.worker.Run(ctx)
	}()

	srv := &http.Server{Addr: httpAddr, Handler: a.handler, ReadHeaderTimeout: 5 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- srv.ListenAndServe() }()
	log.Info("listening", "addr", httpAddr)

	code := 0
	select {
	case err := <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "error", err)
			code = 1
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("shutdown", "error", err)
			code = 1
		}
	}
	stop()
	workerDone.Wait() // the task being run, if any, is recorded before the pool closes
	return code
}
