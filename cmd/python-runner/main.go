package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"classroom-agent/internal/runner"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config := runner.DefaultConfig()
	config.ListenAddr = env("RUNNER_LISTEN_ADDR", config.ListenAddr)
	config.Token = os.Getenv("RUNNER_TOKEN")
	config.StoragePath = env("RUNNER_STORAGE_PATH", config.StoragePath)
	config.DockerBinary = env("RUNNER_DOCKER_BINARY", config.DockerBinary)
	config.PythonImage = env("RUNNER_PYTHON_IMAGE", config.PythonImage)
	config.ExecutionTimeout = envDuration("RUNNER_EXECUTION_TIMEOUT", config.ExecutionTimeout)
	config.ArtifactTTL = envDuration("RUNNER_ARTIFACT_TTL", config.ArtifactTTL)
	config.Concurrency = envInt("RUNNER_CONCURRENCY", config.Concurrency)
	config.QueueCapacity = envInt("RUNNER_QUEUE_CAPACITY", config.QueueCapacity)
	config.MaxCodeBytes = envInt64("RUNNER_MAX_CODE_BYTES", config.MaxCodeBytes)
	config.MaxStdinBytes = envInt64("RUNNER_MAX_STDIN_BYTES", config.MaxStdinBytes)
	config.MaxOutputBytes = envInt64("RUNNER_MAX_OUTPUT_BYTES", config.MaxOutputBytes)
	config.MaxFileBytes = envInt64("RUNNER_MAX_FILE_BYTES", config.MaxFileBytes)
	config.MaxFiles = envInt("RUNNER_MAX_FILES", config.MaxFiles)
	config.Memory = env("RUNNER_MEMORY", config.Memory)
	config.CPUs = env("RUNNER_CPUS", config.CPUs)
	config.PIDs = envInt("RUNNER_PIDS", config.PIDs)
	service, err := runner.New(config, logger)
	if err != nil {
		logger.Error("invalid runner configuration", "error", err)
		os.Exit(2)
	}
	logger.Info("python runner started", "listen", config.ListenAddr, "storage", config.StoragePath, "concurrency", config.Concurrency, "queue_capacity", config.QueueCapacity)
	server := &http.Server{Addr: config.ListenAddr, Handler: service.Routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err = <-serveErr:
	case sig := <-stop:
		logger.Info("python runner shutdown requested", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = server.Shutdown(ctx)
		cancel()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("python runner stopped", "error", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if value, err := time.ParseDuration(os.Getenv(name)); err == nil {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) int64 {
	if value, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil {
		return value
	}
	return fallback
}
