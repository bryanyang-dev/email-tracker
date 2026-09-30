package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/httpapi"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	credentialStore, err := credentials.NewKeychainStore("com.localemailworkspace.gmail", "primary-account")
	if err != nil {
		slog.Error("credential store unavailable", "error", err)
		os.Exit(1)
	}

	outboundClient := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	api := httpapi.New(cfg, credentialStore, outboundClient)
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info("local email service listening", "address", "http://"+cfg.Address, "gmail_configured", cfg.GmailConfigured())
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("local email service stopped", "error", err)
		os.Exit(1)
	}
}
