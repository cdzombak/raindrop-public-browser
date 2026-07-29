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

	"github.com/cdzombak/raindrop-public-browser/internal/config"
	"github.com/cdzombak/raindrop-public-browser/internal/covers"
	"github.com/cdzombak/raindrop-public-browser/internal/oauthstate"
	"github.com/cdzombak/raindrop-public-browser/internal/raindrop"
	"github.com/cdzombak/raindrop-public-browser/internal/refresh"
	"github.com/cdzombak/raindrop-public-browser/internal/render"
	"github.com/cdzombak/raindrop-public-browser/internal/store"
	"github.com/cdzombak/raindrop-public-browser/internal/webserver"
)

func userAgent() string {
	return "raindrop-public-browser/" + version + " (+https://github.com/cdzombak/raindrop-public-browser)"
}

func runServe(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for env, val := range map[string]string{
		config.EnvOAuthStateFile: cfg.OAuthStateFile,
		config.EnvDBDir:          cfg.DBDir,
		config.EnvTemplateDir:    cfg.TemplateDir,
		config.EnvImagesDir:      cfg.ImagesDir,
	} {
		if val == "" {
			return fmt.Errorf("%s must be set for serve", env)
		}
	}

	// Refuse to run without OAuth state; an expired refresh token, by
	// contrast, only fails refreshes while serving continues.
	state, err := oauthstate.Load(cfg.OAuthStateFile)
	if err != nil {
		if errors.Is(err, oauthstate.ErrMissing) {
			return fmt.Errorf("OAuth state file %s is missing or empty; run the login subcommand first", cfg.OAuthStateFile)
		}
		return err
	}

	// Templates load once at startup; a missing or malformed template means
	// exiting non-zero rather than starting and serving errors.
	renderer, err := render.Load(cfg.TemplateDir, render.Config{
		PerPage:    cfg.PerPage,
		BaseURL:    cfg.BaseURL,
		DateFormat: cfg.DateFormat,
		Location:   cfg.Location,
		Version:    version,
	})
	if err != nil {
		return err
	}
	// Parsing proves only syntax, and the startup prerender below exercises
	// just the empty state when the database is new. Execute every template
	// branch now so a broken one exits non-zero instead of serving an
	// endlessly empty site.
	if err := renderer.Verify(); err != nil {
		return fmt.Errorf("template check failed: %w", err)
	}

	st, err := store.Open(cfg.DBDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// Startup order: prerender from the existing database, start serving,
	// then kick off a refresh.
	snap, err := renderer.Snapshot(context.Background(), st, time.Now())
	if err != nil {
		return fmt.Errorf("initial prerender: %w", err)
	}

	srv := webserver.New(st, renderer, cfg.ImagesDir, cfg.BaseURL, snap, logger)

	tokens, err := refresh.NewOAuthTokenSource(cfg.OAuthStateFile, state, logger)
	if err != nil {
		return err
	}
	refresher := &refresh.Refresher{
		Store:   st,
		Fetcher: &raindrop.Fetcher{UserAgent: userAgent()},
		Downloader: &covers.Downloader{
			ImagesDir: cfg.ImagesDir,
			UserAgent: userAgent(),
			Logger:    logger,
		},
		Renderer:   renderer,
		Tokens:     tokens,
		Tag:        cfg.Tag,
		Logger:     logger,
		OnSnapshot: srv.SetSnapshot,
	}

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
		// Explicit timeouts; the zero values are unbounded.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	refreshCtx, cancelRefresh := context.WithCancel(context.Background())
	defer cancelRefresh()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		refresher.Run(refreshCtx, cfg.RefreshInterval, srv.SetLastRefreshOK)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("serving", "addr", cfg.ListenAddr, "version", version,
			"tag", cfg.Tag, "refresh_interval", cfg.RefreshInterval.String())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		cancelRefresh()
		return err
	case s := <-sig:
		logger.Info("shutting down", "signal", s.String())
	}

	// Stop accepting connections and let in-flight requests finish; an
	// in-progress refresh is cancelled rather than waited on — a partial
	// refresh is harmless because the next startup refreshes again.
	cancelRefresh()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown did not complete cleanly", "error", err)
	}
	<-refreshDone
	return nil
}
