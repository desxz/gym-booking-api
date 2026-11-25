package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/muratgun/gym-booking-api/internal/auth"
	"github.com/muratgun/gym-booking-api/internal/config"
	"github.com/muratgun/gym-booking-api/internal/httpapi"
	"github.com/muratgun/gym-booking-api/internal/repository"
	"github.com/muratgun/gym-booking-api/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped with error", "error", err)
		os.Exit(1)
	}
}

// run owns all resources so every defer executes before main exits.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.Database.ConnectTimeout)
	defer cancelStartup()
	pool, err := repository.NewPostgresPool(startupCtx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	// Registered first so the pool closes last, after the HTTP server drains.
	defer pool.Close()

	repo := repository.NewPostgres(pool, cfg.Database.RollbackTimeout)
	tokens := auth.NewTokenManager(cfg.Auth.JWTIssuer, cfg.Auth.AccessTokenTTL, cfg.Auth.JWTSecret)
	refreshTokens := auth.NewRefreshTokenManager(cfg.Auth.RefreshTokenTTL)
	authService := service.NewAuthService(service.AuthDeps{
		Users:              repo,
		RefreshTokens:      repo,
		Hasher:             auth.NewHasher(cfg.Auth.BcryptCost),
		AccessTokens:       tokens,
		RefreshTokenIssuer: refreshTokens,
		DummyPasswordHash:  auth.DummyPasswordHash,
	})
	bookingService := service.NewBookingService(repo, nil)

	var ready atomic.Bool
	ready.Store(true)

	router := httpapi.NewRouter(httpapi.RouterConfig{
		Auth:             authService,
		Bookings:         bookingService,
		Tokens:           tokens,
		Logger:           logger,
		RequestTimeout:   cfg.HTTP.RequestTimeout,
		MaxBodyBytes:     cfg.HTTP.MaxBodyBytes,
		ReadyPingTimeout: cfg.Database.ReadyPingTimeout,
		ReadyCheck: func(ctx context.Context) error {
			if !ready.Load() {
				return errors.New("shutting down")
			}
			pingCtx, cancel := context.WithTimeout(ctx, cfg.Database.ReadyPingTimeout)
			defer cancel()
			return pool.Ping(pingCtx)
		},
	})

	server := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()
	logger.Info("http server listening", "addr", cfg.HTTP.Addr)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-signalCtx.Done():
	}

	logger.Info("shutdown signal received")
	ready.Store(false)

	if cfg.HTTP.ShutdownDrainDelay > 0 {
		logger.Info("draining before shutdown", "delay", cfg.HTTP.ShutdownDrainDelay)
		time.Sleep(cfg.HTTP.ShutdownDrainDelay)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("http server stopped")
	return nil
}
