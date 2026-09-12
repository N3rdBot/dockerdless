// Command dockerdless provides the Docker-compatible daemon entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/N3rdBot/dockerdless/internal/api"
	"github.com/N3rdBot/dockerdless/internal/config"
	"github.com/N3rdBot/dockerdless/internal/observability"
	"go.uber.org/zap"
)

const shutdownTimeout = 5 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dockerdless: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("dockerdless", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: dockerdless [options]")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Run the Docker-compatible dockerdless daemon.")
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}
	help := flags.Bool("help", false, "print this help message")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse command-line flags: %w", err)
	}
	if *help {
		flags.Usage()
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	runtime, err := observability.Bootstrap(observability.BootstrapConfig{
		ServiceName:  cfg.OTelServiceName,
		LogLevel:     cfg.LogLevel,
		OTLPEndpoint: cfg.OTelEndpoint,
	})
	if err != nil {
		return fmt.Errorf("initialize observability: %w", err)
	}
	logger := runtime.Logger()

	server, err := api.NewServer(cfg.SocketPath, api.NewRouter())
	if err != nil {
		return errors.Join(
			fmt.Errorf("initialize API server: %w", err),
			runtime.Shutdown(context.Background()),
		)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("dockerdless daemon started", zap.String("socket", server.SocketPath()))
	<-ctx.Done()
	logger.Info("dockerdless daemon shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return runtime.Shutdown(shutdownCtx)
}
