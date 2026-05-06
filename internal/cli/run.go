package cli

import (
	"context"
	"errors"
	"io"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/logger"
	"github.com/spf13/cobra"
)

const shutdownGracePeriod = 5 * time.Second

func newRunCmd(stdout, _ io.Writer) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run skeeper services with the supplied configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runE(cmd.Context(), configPath, stdout)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to TOML config file (defaults to $SKEEPER_CONFIG)")
	return cmd
}

func runE(parent context.Context, configPath string, _ io.Writer) error {
	if err := config.LoadDotEnvIfPresent(config.DefaultDotEnvPath); err != nil {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log, err := logger.New(cfg.Log.Level)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("starting", "app", cfg.App.Name, "env", cfg.App.Env)

	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	launch := func(fn func() error) {
		wg.Go(func() {
			errCh <- fn()
		})
	}
	_ = launch

	// TODO: launch your services here.
	// launch(func() error { return myService.Run(ctx) })

	<-ctx.Done()
	log.Info("shutdown signal received")
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
		close(errCh)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGracePeriod):
		log.Warn("shutdown deadline exceeded")
	}

	var runErr error
	for err := range errCh {
		if err == nil || errors.Is(err, context.Canceled) {
			continue
		}
		if runErr == nil {
			runErr = err
		}
	}
	return runErr
}
