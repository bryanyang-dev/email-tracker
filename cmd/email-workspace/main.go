package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/httpapi"
	"local-email-workspace/internal/ollama"
	storagesqlite "local-email-workspace/internal/storage/sqlite"
)

func main() {
	if err := run(); err != nil {
		slog.Error("local email service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	database, err := storagesqlite.Open(filepath.Join(cfg.DataDirectory, "workspace.sqlite"))
	if err != nil {
		return fmt.Errorf("workspace database unavailable: %w", err)
	}
	defer database.Close()
	slog.Info("workspace database ready")

	credentialStore, err := credentials.NewKeychainStore("com.localemailworkspace.gmail", "primary-account")
	if err != nil {
		return fmt.Errorf("credential store unavailable: %w", err)
	}

	outboundClient := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	applicationContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	ollamaClient := ollama.NewClient(cfg.OllamaBaseURL, outboundClient)
	ollamaRuntime := ollama.NewRuntime(ollamaClient, cfg.OllamaBaseURL)
	if cfg.OllamaAutoStart {
		startupContext, cancelStartup := context.WithTimeout(applicationContext, 10*time.Second)
		started, startErr := ollamaRuntime.EnsureRunning(startupContext)
		cancelStartup()
		if startErr != nil {
			slog.Warn("Ollama unavailable; continuing without local AI", "error", startErr)
		} else if started {
			slog.Info("started local Ollama service", "address", cfg.OllamaBaseURL)
		}
	}

	api := httpapi.NewWithRepository(cfg, credentialStore, outboundClient, database)
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	slog.Info("local email service listening", "address", "http://"+cfg.Address, "gmail_configured", cfg.GmailConfigured())
	var serveErr error
	select {
	case <-applicationContext.Done():
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			serveErr = err
		}
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		slog.Error("local email service shutdown failed", "error", err)
	}
	if err := ollamaRuntime.Stop(shutdownContext); err != nil && err != context.DeadlineExceeded {
		slog.Warn("Ollama shutdown failed", "error", err)
	}
	return serveErr
}
