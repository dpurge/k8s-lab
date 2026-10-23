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

	"assistant/internal/a2ui"
	"assistant/internal/bus"
	"assistant/internal/callctx"
	"assistant/internal/chat"
	"assistant/internal/config"
	"assistant/internal/db"
	"assistant/internal/llm"
	"assistant/internal/llm/demo"
	"assistant/internal/llmcalls"
	"assistant/internal/metrics"
	"assistant/internal/server"
	"assistant/internal/tasks"
	"assistant/internal/tasksteps"
	"assistant/internal/tools"
	"assistant/internal/users"
	"assistant/internal/workflow"
	"assistant/internal/workflows"
	sharedbus "k8s-lab/shared/bus"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/retention"
	"k8s-lab/shared/router/anthropic"
	"k8s-lab/shared/router/ollama"
	"k8s-lab/shared/router/openai"
	"k8s-lab/shared/router/vertex"
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
	pool    *pgxpool.Pool
	metrics *metrics.Metrics
	log     *slog.Logger
	// bus calls other apps over NATS; nil when ASSISTANT_NATS_URL is not set
	// or the connection failed. No workflow tool uses it yet.
	bus *bus.Client
}

// modelSetup is the model configuration and the providers that serve it,
// keyed by provider type. Tests pass scripted providers.
type modelSetup struct {
	config    *modelcfg.Config
	providers map[string]llm.Provider
}

// loadModels reads the models file; its error names the setting and the file
// so a misconfigured deployment is fixed at once. A credential that is missing
// is not an error: the router skips that provider until the credential
// appears, and says so in its log.
func loadModels(path string) (modelSetup, error) {
	cfg, err := modelcfg.Load(path)
	if err != nil {
		return modelSetup{}, fmt.Errorf("%s (%s): %w", config.EnvModelsFile, path, err)
	}
	return modelSetup{config: cfg, providers: map[string]llm.Provider{
		"ollama": ollama.New(), "demo": demo.New(), "vertex": vertex.New(), "openai": openai.New(), "anthropic": anthropic.New(),
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

// callLogAttrs adds who a model call is for to the router's log lines: ids
// only, never a name or an email.
func callLogAttrs(ctx context.Context) []slog.Attr {
	info, ok := callctx.From(ctx)
	if !ok {
		return nil
	}
	attrs := []slog.Attr{slog.String("user_id", info.UserID)}
	if info.TaskID != "" {
		attrs = append(attrs, slog.String("task_id", info.TaskID))
	}
	return attrs
}

// runCanceller ends a conversation's run with the routine the HTTP cancel uses.
type runCanceller struct {
	store  *tasks.Store
	worker *tasks.Worker
}

func (c runCanceller) Cancel(ctx context.Context, taskID string) (tasks.Task, tasks.CancelOutcome, error) {
	return tasks.CancelOrStop(ctx, c.store, c.worker, taskID)
}

// newApp wires the stores, the model router, the workflow engine, the worker
// and the HTTP handler together. The clock is how the noop workflow waits;
// tests pass a fake. wfs are the YAML workflows tasks can name.
func newApp(pool *pgxpool.Pool, log *slog.Logger, clock workflows.Clock, setup modelSetup, wfs map[string]*workflow.Workflow, mediaHosts []string) (*app, error) {
	m := metrics.New()
	taskStore := tasks.NewStore(pool)
	callStore := llmcalls.NewStore(pool)

	recorder := llmcalls.NewRecorder(setup.config, callStore, m, log)
	router, err := llm.NewRouter(setup.config, setup.providers, llm.WithObserver(recorder), llm.RequireAttribution(),
		llm.WithLogger(log), llm.WithLogAttrs(callLogAttrs), llm.WithAvailability(llm.CredentialAvailability(os.Getenv, os.ReadFile)))
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
	uiValidator, err := a2ui.New()
	if err != nil {
		return nil, fmt.Errorf("A2UI schemas: %w", err)
	}
	// A workflow that declares ui is taught the catalog, and an interface it
	// writes is checked before its reply is accepted.
	prompts.UIGuide = uiValidator.Guide()
	prompts.UISchema = uiValidator.InterfaceSchema()
	stepStore := tasksteps.NewStore(pool)
	engine := &workflow.Engine{
		LLM: router, Tools: toolRegistry, Prompts: prompts, UI: uiValidator,
		Recorder: tasksteps.NewRecorder(stepStore, m, log),
	}
	engineRunner := &workflows.EngineRunner{Engine: engine, Workflows: wfs}
	engine.Delegates = engineRunner // the workflows that declare intents are what the orchestrator routes to
	runner := workflows.NewRunner(workflows.New(clock, workflows.WithLLM(router)), engineRunner)

	conversations := &chat.Service{
		Store: chat.NewStore(pool), Tasks: taskStore, Catalog: runner,
		Channel: &chat.MockChannel{}, Errors: m, Rejections: m, UI: uiValidator, Log: log,
	}
	worker := &tasks.Worker{Queue: taskStore, Runner: runner, Observer: m, Notifier: conversations, Log: log}
	conversations.Canceller = runCanceller{store: taskStore, worker: worker}

	return &app{
		handler: server.NewRouter(server.Deps{
			DB: pool, Users: users.NewStore(pool), Tasks: taskStore, Calls: callStore, Steps: stepStore, Worker: worker, Workflows: runner, Chat: conversations, Models: router, A2UIMediaHosts: mediaHosts, Metrics: m, Log: log,
		}),
		worker:  worker,
		tasks:   taskStore,
		pool:    pool,
		metrics: m,
		log:     log,
	}, nil
}

// connectBus connects to NATS when url is set and keeps the client in a.bus.
// An empty url means no connection and no client. A failed connection must not
// stop the assistant, which does not depend on the bus: it warns and goes on
// without a client. The warning has a fixed text because the connection error
// could carry the URL, which may hold credentials.
func (a *app) connectBus(url string) {
	if !sharedbus.Enabled(url) {
		return
	}
	client, err := bus.Dial(url, a.log, bus.WithObserver(a.metrics))
	if err != nil {
		a.log.Warn("could not connect to NATS; continuing without calls to other apps (check ASSISTANT_NATS_URL and that the server is reachable)")
		return
	}
	a.bus = client
	a.log.Info("connected to NATS")
}

// closeBus drains the NATS connection, if there is one.
func (a *app) closeBus() {
	if err := a.bus.Close(); err != nil {
		a.log.Warn("closing the NATS connection", "error", err)
	}
}

// newPurger returns the loop that deletes model calls older than keep days
// (0 keeps them forever). tasks and task_steps are deliberately not purged.
func (a *app) newPurger(keepDays int) (*retention.Purger, error) {
	keep := time.Duration(keepDays) * 24 * time.Hour
	return retention.NewPurger(a.pool, []retention.Rule{llmcalls.RetentionRule(keep)},
		retention.WithOnPurged(a.metrics.Purged), retention.WithLogger(a.log))
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

	setup, err := loadModels(cfg.ModelsFile)
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
	a, err := newApp(pool, log, workflows.RealClock{}, setup, wfs, cfg.A2UIMediaHosts)
	if err != nil {
		log.Error("startup", "error", err)
		return 1
	}
	if err := a.recoverCrashed(ctx); err != nil {
		log.Error("startup", "error", err)
		return 1
	}

	a.connectBus(cfg.NATSURL)
	defer a.closeBus()

	var workerDone sync.WaitGroup
	workerDone.Add(1)
	go func() {
		defer workerDone.Done()
		a.worker.Run(ctx)
	}()

	purger, err := a.newPurger(cfg.LLMCallsRetentionDays)
	if err != nil {
		log.Error("startup", "error", err)
		return 1
	}
	workerDone.Add(1)
	go func() {
		defer workerDone.Done()
		purger.Run(ctx)
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
