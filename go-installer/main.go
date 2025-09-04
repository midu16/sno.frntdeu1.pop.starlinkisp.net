package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"openshift-sno-hub-installer/internal/app"
	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/logger"
)

func main() {
	// Initialize logger
	log := logger.NewLogger()

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create application instance
	application := app.NewApp(cfg, log)

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info("Received shutdown signal, cleaning up...")
		cancel()
	}()

	// Run the application
	if err := application.Run(ctx); err != nil {
		log.Fatalf("Application failed: %v", err)
	}

	log.Info("Application completed successfully")
}