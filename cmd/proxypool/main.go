package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/john/proxypool/internal/admin"
	"github.com/john/proxypool/internal/buildcheck"
	"github.com/john/proxypool/internal/config"
	"github.com/john/proxypool/internal/manager"
)

func main() {
	if err := run(); err != nil {
		slog.Error("proxypool stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := buildcheck.VerifyBuildTags(); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, serveDeps{
		cfg:     cfg,
		logger:  logger,
		listen:  net.Listen,
		newPool: manager.New,
		bootstrap: func(ctx context.Context, pool *manager.Manager) error {
			return pool.Bootstrap(ctx)
		},
		closePool: func(pool *manager.Manager) error {
			return pool.Close()
		},
	})
}

type serveDeps struct {
	cfg       *config.Config
	logger    *slog.Logger
	listen    func(network, address string) (net.Listener, error)
	newPool   func(cfg *config.Config, logger *slog.Logger) *manager.Manager
	bootstrap func(ctx context.Context, pool *manager.Manager) error
	closePool func(pool *manager.Manager) error
}

func serve(ctx context.Context, deps serveDeps) error {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(deps.cfg.StatusPort))
	listener, err := deps.listen("tcp", address)
	if err != nil {
		return fmt.Errorf("status server listen %s: %w", address, err)
	}
	defer listener.Close()

	deps.logger.Info("status server started", "address", address)

	pool := deps.newPool(deps.cfg, deps.logger)
	defer deps.closePool(pool)

	server := &http.Server{
		Addr:    address,
		Handler: admin.New(pool),
	}
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(listener)
	}()

	bootCtx, cancelBootstrap := context.WithCancel(ctx)
	defer cancelBootstrap()
	bootErrCh := make(chan error, 1)
	go func() {
		bootErrCh <- deps.bootstrap(bootCtx, pool)
	}()

	var bootErr error
	select {
	case <-ctx.Done():
		cancelBootstrap()
		<-bootErrCh
		_ = server.Close()
		return nil
	case bootErr = <-bootErrCh:
	case serveErr := <-serveErrCh:
		cancelBootstrap()
		<-bootErrCh
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("status server: %w", serveErr)
	}
	if bootErr != nil {
		_ = server.Close()
		<-serveErrCh
		return fmt.Errorf("bootstrap pool: %w", bootErr)
	}

	select {
	case <-ctx.Done():
		if err := server.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("status server shutdown: %w", err)
		}
		return nil
	case err := <-serveErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("status server: %w", err)
	}
}
