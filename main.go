package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	signals            = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	shutdownPeriod     = 15 * time.Second
	shutdownHardPeriod = 3 * time.Second
	timeSleep          = time.Sleep
)

func main() {
	// Reminder: `defer` doesn't behave as expected in functions with log.Fatal, os.Exit, etc.
	rootCtx := context.Background()

	// TODO set up slog in context

	app := app()

	if err := runApp(rootCtx, ":8080", app); err != nil {
		slog.ErrorContext(rootCtx, "failed to run app", "error", err)
		os.Exit(1)
	}
}

func app() http.Handler {
	mux := http.NewServeMux()
	// Example readiness endpoint
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		// Kubernetes will set the pod state to terminating, removing it from the svc endpoint
		w.WriteHeader(http.StatusOK)
	})
	// Example business logic
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			_, _ = fmt.Fprintln(w, "Hello, world!")
		case <-r.Context().Done():
			http.Error(w, "Request cancelled.", http.StatusRequestTimeout)
		}
	})
	return mux
}

func runApp(ctx context.Context, addr string, handler http.Handler) error {
	rootCtx, cancelRoot := signal.NotifyContext(ctx, signals...)
	defer cancelRoot()

	// In-flight requests get a context that won't be immediately cancelled on SIGINT/SIGTERM
	// so that they can be gracefully stopped.
	ongoingCtx, cancelOngoing := context.WithCancel(context.WithoutCancel(rootCtx))
	server := &http.Server{
		Addr: addr,
		BaseContext: func(_ net.Listener) context.Context {
			return ongoingCtx
		},
		Handler: handler,
	}

	errCh := make(chan error)
	go func() {
		defer close(errCh)
		slog.InfoContext(rootCtx, "Server listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		slog.InfoContext(rootCtx, "Received shutdown signal, shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			cancelOngoing()
			// TODO handler would need to implement closable and get called here to clean up external resources (e.g. DB connections)
			return err
		}
	}

	slog.InfoContext(rootCtx, "Waiting for ongoing requests to finish")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(rootCtx), shutdownPeriod)
	defer cancelShutdown()
	err := server.Shutdown(shutdownCtx)
	cancelOngoing()
	if err != nil {
		slog.ErrorContext(rootCtx, "Failed to wait for ongoing requests to finish, waiting for forced cancellation")
		timeSleep(shutdownHardPeriod)
	}

	// TODO handler would need to implement closable and get called here to clean up external resources (e.g. DB connections)

	slog.InfoContext(rootCtx, "Server shut down")
	return err
}
