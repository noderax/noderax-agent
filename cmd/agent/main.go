package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/noderax/noderax-agent/internal/agent"
	"github.com/noderax/noderax-agent/internal/api"
	"github.com/noderax/noderax-agent/internal/config"
	"github.com/noderax/noderax-agent/internal/logger"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	log.Info("starting noderax agent", "version", version, "commit", commit, "build_date", buildDate)

	client := api.NewClient(cfg.APIURL, cfg.RequestTimeout)
	if cfg.AgentToken != "" {
		client.SetAgentToken(cfg.AgentToken)
	}

	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		if err := agent.RunInteractiveEnrollment(ctx, cfg, client, log, version, os.Stdin, os.Stdout); err != nil {
			log.Error("interactive enrollment failed", "error", err)
			os.Exit(1)
		}

		log.Info("interactive enrollment completed")
		return
	}

	svc := agent.NewService(cfg, client, log, version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := svc.Run(ctx); err != nil {
		log.Error("agent exited with error", "error", err)
		os.Exit(1)
	}

	log.Info("agent stopped")
}
