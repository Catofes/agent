package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"classroom-agent/internal/agent"
	"classroom-agent/internal/config"
	"classroom-agent/internal/server"
	"classroom-agent/internal/store"
	"classroom-agent/internal/tools"
)

var version = "dev"

//go:embed web data/templates
var assets embed.FS

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(version)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	if err = os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o750); err != nil {
		logger.Error("create data directory", "error", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	run, err := st.EnsureActiveRun(context.Background(), newRunID(), cfg.InitialRunName)
	if err != nil {
		logger.Error("ensure active run", "error", err)
		os.Exit(1)
	}
	if _, err = st.ImportStudentsCSV(context.Background(), run.ID, cfg.StudentsCSV); err != nil {
		logger.Error("import students", "error", err, "path", cfg.StudentsCSV)
		os.Exit(1)
	}
	toolItems := []tools.Tool{tools.Calculator{}, tools.NewWebFetch()}
	if strings.TrimSpace(cfg.BraveSearchAPIKey) != "" {
		toolItems = append(toolItems, tools.NewBraveSearch(cfg.BraveSearchAPIKey))
	}
	registry := tools.NewRegistry(toolItems...)
	client := &agent.DeepSeekClient{BaseURL: cfg.DeepSeekBaseURL, APIKey: cfg.DeepSeekAPIKey, HTTP: &http.Client{Transport: &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 50, IdleConnTimeout: 90 * time.Second}}}
	engine := agent.NewEngine(st, client, registry, cfg.DeepSeekModel, cfg.AnonymousHMACKey, cfg.LLMTimeout, cfg.LLMConcurrency)
	engine.TokenBudget = cfg.StudentTokenBudget
	engine.MaxToolCalls = cfg.MaxToolCalls
	engine.MaxToolCallsByName = map[string]int{"web_search": 2, "web_fetch": 2}
	engine.MaxOutputChars = cfg.MaxOutputChars
	engine.MaxReasoningChars = cfg.MaxReasoningChars
	engine.MemoryExtractor = agent.LLMMemoryExtractor{Client: client, Model: cfg.DeepSeekModel}
	engine.MemoryExtractTimeout = cfg.MemoryExtractTimeout
	engine.MaxMemoryItems = cfg.MaxMemoryItems
	engine.MaxMemoryChars = cfg.MaxMemoryChars
	engine.MaxMemoryTokens = cfg.MaxMemoryTokens
	engine.InputPricePerM = cfg.InputPricePerM
	engine.OutputPricePerM = cfg.OutputPricePerM
	webFS, _ := fs.Sub(assets, "web")
	templateFS, _ := fs.Sub(assets, "data/templates")
	app := server.New(cfg, st, engine, webFS, templateFS, logger)
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server started", "listen", cfg.ListenAddr, "version", version, "run_id", run.ID)
		e := httpServer.ListenAndServe()
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		serveErr <- e
	}()
	select {
	case e := <-serveErr:
		if e != nil {
			logger.Error("server stopped unexpectedly", "error", e)
		}
		return
	case sig := <-stop:
		logger.Info("shutdown requested", "signal", sig.String())
	}

	app.Shutdown()
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- httpServer.Shutdown(ctx)
	}()
	select {
	case e := <-shutdownDone:
		if e != nil {
			logger.Error("graceful shutdown failed", "error", e)
			if closeErr := httpServer.Close(); closeErr != nil {
				logger.Error("force close after shutdown failure", "error", closeErr)
			}
		}
	case sig := <-stop:
		logger.Warn("second shutdown signal received; forcing close", "signal", sig.String())
		if e := httpServer.Close(); e != nil {
			logger.Error("force close failed", "error", e)
		}
	}
	logger.Info("server stopped")
}

func newRunID() string { return "run_" + time.Now().UTC().Format("20060102T150405.000000000") }
