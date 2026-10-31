package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // zona horaria embebida: la imagen no depende de tzdata del sistema

	"github.com/jcrlabs/barber-back/internal/config"
	"github.com/jcrlabs/barber-back/internal/database"
	"github.com/jcrlabs/barber-back/internal/domain"
	"github.com/jcrlabs/barber-back/internal/repository"
	"github.com/jcrlabs/barber-back/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		slog.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()

	if err := database.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Subcomando de operación: `server promote-admin <email>` (kubectl exec).
	if len(os.Args) == 3 && os.Args[1] == "promote-admin" {
		users := repository.NewUserRepository(db)
		u, err := users.FindByEmail(ctx, os.Args[2])
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		if err := users.SetRole(ctx, u.ID, domain.RoleAdmin); err != nil {
			return err
		}
		slog.Info("user promoted to admin", slog.String("user_id", u.ID.String()))
		return nil
	}

	if cfg.SeedDemoData {
		if err := server.SeedDemo(ctx, repository.NewCatalogRepository(db)); err != nil {
			slog.Warn("demo seed failed", slog.String("error", err.Error()))
		}
	}

	if cfg.BootstrapAdminLogin != "" && cfg.BootstrapAdminPassword != "" {
		if err := server.BootstrapAdmin(ctx, repository.NewUserRepository(db), cfg.BootstrapAdminLogin, cfg.BootstrapAdminPassword); err != nil {
			slog.Warn("bootstrap admin failed", slog.String("error", err.Error()))
		}
	}

	app, err := server.New(cfg, db, logger)
	if err != nil {
		return err
	}

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := app.Tokens.PurgeExpired(ctx); err != nil {
					slog.Error("purge refresh tokens", slog.String("error", err.Error()))
				} else if n > 0 {
					slog.Info("expired refresh tokens purged", slog.Int64("count", n))
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", slog.String("port", cfg.Port), slog.String("env", cfg.Env))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
