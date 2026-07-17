package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/api"
	"github.com/kubesqueeze/kubesqueezeagent/internal/cli"
	"github.com/kubesqueeze/kubesqueezeagent/internal/config"
	"github.com/kubesqueeze/kubesqueezeagent/internal/database"
	"github.com/kubesqueeze/kubesqueezeagent/internal/kube"
	prom "github.com/kubesqueeze/kubesqueezeagent/internal/prometheus"
	"github.com/kubesqueeze/kubesqueezeagent/internal/worker"
	"github.com/kubesqueeze/kubesqueezeagent/internal/workload"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	cfg := config.Load()
	switch args[0] {
	case "migrate":
		db, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		return database.Migrate(ctx, db)
	case "server":
		db, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := database.WaitForMigrations(ctx, db); err != nil {
			return err
		}
		return api.New(cfg, db).Run(ctx)
	case "collector", "executor":
		db, err := database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := database.WaitForMigrations(ctx, db); err != nil {
			return err
		}
		kubeClient, err := kube.New(cfg.ClusterID, cfg.Kubeconfig, cfg.PrometheusURL)
		if err != nil {
			return err
		}
		if args[0] == "collector" {
			return worker.NewCollector(cfg, db, kubeClient).Run(ctx)
		}
		return worker.NewExecutor(cfg, db, kubeClient).Run(ctx)
	case "workload":
		return workload.Run(ctx)
	case "seed-metrics":
		client := prom.New(cfg.PrometheusURL)
		ready := false
		for attempt := 0; attempt < 60; attempt++ {
			if client.Ready(ctx) == nil {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if !ready {
			return fmt.Errorf("prometheus did not become ready")
		}
		return prom.SeedHistory(ctx, cfg.PrometheusURL)
	case "squeeze":
		set := flag.NewFlagSet("squeeze", flag.ContinueOnError)
		apiURL := set.String("api-url", "http://127.0.0.1:8080", "KubeSqueeze API URL")
		recommendation := set.String("recommendation", "", "approved recommendation ID")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if *recommendation == "" {
			return fmt.Errorf("--recommendation is required")
		}
		return cli.Post(*apiURL, "/api/v1/recommendations/"+*recommendation+"/execute")
	case "restore":
		set := flag.NewFlagSet("restore", flag.ContinueOnError)
		apiURL := set.String("api-url", "http://127.0.0.1:8080", "KubeSqueeze API URL")
		execution := set.String("execution", "", "execution ID")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if *execution == "" {
			return fmt.Errorf("--execution is required")
		}
		return cli.Post(*apiURL, "/api/v1/executions/"+*execution+"/restore")
	default:
		return usage()
	}
}

func usage() error {
	fmt.Fprintln(os.Stderr, "usage: kubesqueeze <migrate|server|collector|executor|workload|seed-metrics|squeeze|restore>")
	return fmt.Errorf("command is required")
}
