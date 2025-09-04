package main

import (
	"context"
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
	defer log.Close()

	// Handle config and help commands specially (no config validation needed)
	if len(os.Args) > 1 && (os.Args[1] == "config" || os.Args[1] == "help") {
		application := app.NewEnhancedApp(&config.Config{}, log)
		ctx := context.Background()
		if err := application.Run(ctx); err != nil {
			log.Fatalf("Application failed: %v", err)
		}
		return
	}

	// Load configuration for other commands
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create application instance
	application := app.NewEnhancedApp(cfg, log)

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