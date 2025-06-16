package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestRunApp_shouldShutdownGracefullyOnSignal(t *testing.T) {
	oldSignals := signals
	t.Cleanup(func() {
		signals = oldSignals
	})
	// We need to use a signal that Go testing won't be listening for
	signals = []os.Signal{syscall.SIGHUP}

	h := &slowRequestHandler{wait: 1500 * time.Millisecond}

	port := freePort(t)

	errChan := make(chan error)
	go func() {
		defer close(errChan)
		err := runApp(t.Context(), fmt.Sprintf(":%d", port), h)
		errChan <- err
	}()

	require.Eventuallyf(t, func() bool {
		r, err := http.Get(fmt.Sprintf("http://localhost:%d/quick", port))
		return err == nil && r.StatusCode == http.StatusOK
	}, time.Second, 100*time.Millisecond, "Server did not start up")

	stopRequests := make(chan struct{})
	requestGroup := errgroup.Group{}
	go func() {
		url := fmt.Sprintf("http://localhost:%d/", port)
		requestTrigger := time.NewTicker(100 * time.Millisecond)
		defer requestTrigger.Stop()
		defer close(stopRequests)
		for {
			select {
			case <-requestTrigger.C:
				requestGroup.Go(func() error {
					res, err := http.Get(url)
					if err != nil {
						return err
					}
					if res.StatusCode != http.StatusOK {
						return fmt.Errorf("unexpected status code %d", res.StatusCode)
					}
					return nil
				})
			case <-stopRequests:
				return
			case <-t.Context().Done():
				return
			}
		}
	}()

	time.Sleep(1550 * time.Millisecond)

	// Kubernetes would put the pod into Terminating _before_ sending SIGINT, so stop sending requests first
	stopRequests <- struct{}{}
	assert.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGHUP))

	err := <-errChan
	require.NoError(t, err)

	err = requestGroup.Wait()
	require.NoError(t, err)

	assert.Empty(t, h.failures)
}

func TestRunApp_shouldFailIfServerCannotStartUp(t *testing.T) {
	l, err := net.ListenTCP("tcp", &net.TCPAddr{Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = l.Close()
	})
	// note - port is still in use
	port := l.Addr().(*net.TCPAddr).Port

	err = runApp(
		t.Context(),
		fmt.Sprintf(":%d", port),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)
	require.Error(t, err)
}

func TestRunApp_shouldFailIfShutdownFails(t *testing.T) {
	oldShutdownHard := shutdownHardPeriod
	t.Cleanup(func() {
		shutdownHardPeriod = oldShutdownHard
	})
	shutdownHardPeriod = 10 * time.Millisecond

	oldShutdownPeriod := shutdownPeriod
	t.Cleanup(func() {
		shutdownPeriod = oldShutdownPeriod
	})
	shutdownPeriod = 10 * time.Millisecond

	sleepCalled := false
	oldSleep := timeSleep
	t.Cleanup(func() {
		timeSleep = oldSleep
	})
	timeSleep = func(d time.Duration) {
		sleepCalled = true
		assert.Equal(t, shutdownHardPeriod, d)
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/quick" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Ignore the request context to make sure the server is always busy during shutdown
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	port := freePort(t)

	errChan := make(chan error)
	serverCtx, serverCancel := context.WithCancel(t.Context())
	defer serverCancel()
	go func() {
		defer close(errChan)
		err := runApp(serverCtx, fmt.Sprintf(":%d", port), h)
		errChan <- err
	}()

	require.Eventuallyf(t, func() bool {
		r, err := http.Get(fmt.Sprintf("http://localhost:%d/quick", port))
		return err == nil && r.StatusCode == http.StatusOK
	}, time.Second, 100*time.Millisecond, "Server did not start up")

	go func() {
		for {
			select {
			case <-t.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
				go func() {
					// Just need to make sure the server is busy when shutdown happens
					_, _ = http.Get(fmt.Sprintf("http://localhost:%d/", port))
				}()
			}
		}
	}()

	// Give the goroutine above time to get the server busy
	time.Sleep(500 * time.Millisecond)

	serverCancel()

	err := <-errChan
	require.Error(t, err)

	require.True(t, sleepCalled)
}

var _ http.Handler = &slowRequestHandler{}

type slowRequestHandler struct {
	failures []string
	wait     time.Duration
}

func (s *slowRequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/quick" {
		w.WriteHeader(http.StatusOK)
		return
	}
	select {
	case <-time.After(s.wait):
		w.WriteHeader(http.StatusOK)
		return
	case <-r.Context().Done():
		s.failures = append(s.failures, r.RemoteAddr)
		http.Error(w, "Request cancelled", http.StatusRequestTimeout)
		return
	}
}

func freePort(t *testing.T) int {
	l, err := net.ListenTCP("tcp", &net.TCPAddr{Port: 0})
	require.NoError(t, err)
	require.NoError(t, l.Close())
	return l.Addr().(*net.TCPAddr).Port
}
