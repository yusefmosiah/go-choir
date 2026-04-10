package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	s := NewServer("test-service", "8099")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	s.handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status \"ok\", got %q", body["status"])
	}
	if body["service"] != "test-service" {
		t.Errorf("expected service \"test-service\", got %q", body["service"])
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type to contain application/json, got %q", ct)
	}
}

func TestHealthHandlerServiceName(t *testing.T) {
	names := []string{"auth", "proxy", "vmctl", "gateway", "sandbox"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			s := NewServer(name, "8099")
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			w := httptest.NewRecorder()
			s.handler(w, req)

			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode response body: %v", err)
			}
			if body["service"] != name {
				t.Errorf("expected service %q, got %q", name, body["service"])
			}
		})
	}
}

func TestHealthHandlerMethodNotAllowed(t *testing.T) {
	s := NewServer("test-service", "8099")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/health", nil)
			w := httptest.NewRecorder()
			s.handler(w, req)

			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected status 405 for %s, got %d", method, w.Code)
			}
		})
	}
}

func TestPortFromEnv(t *testing.T) {
	envVar := "TEST_PORT_FOR_ENV"
	os.Setenv(envVar, "9999")
	defer os.Unsetenv(envVar)

	port := PortFromEnv(envVar, "8081")
	if port != "9999" {
		t.Errorf("expected port 9999, got %q", port)
	}
}

func TestPortDefault(t *testing.T) {
	envVar := "TEST_PORT_DEFAULT_UNSET"
	os.Unsetenv(envVar)

	port := PortFromEnv(envVar, "8081")
	if port != "8081" {
		t.Errorf("expected default port 8081, got %q", port)
	}
}

func TestServerStartAndAcceptsRequests(t *testing.T) {
	s := NewServer("test-service", "0") // port 0 = OS picks a free port

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start()
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Server should be listening now
	addr := s.Addr()
	if addr == "" {
		t.Fatal("server address is empty after start")
	}

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("failed to reach /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Shutdown the server
	s.Shutdown()
	wg.Wait()
}

func TestGracefulShutdownOnSIGTERM(t *testing.T) {
	s := NewServer("test-sigterm", "0")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	addr := s.Addr()
	// Verify server is up
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("server not reachable before SIGTERM: %v", err)
	}
	resp.Body.Close()

	// Send SIGTERM to ourselves
	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGTERM)

	// Wait for server to shut down with a timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: server shut down cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5 seconds of SIGTERM")
	}
}

func TestGracefulShutdownOnSIGINT(t *testing.T) {
	s := NewServer("test-sigint", "0")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	addr := s.Addr()
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("server not reachable before SIGINT: %v", err)
	}
	resp.Body.Close()

	p, _ := os.FindProcess(os.Getpid())
	p.Signal(syscall.SIGINT)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5 seconds of SIGINT")
	}
}

func TestGracefulShutdownWaitsForInFlightRequest(t *testing.T) {
	s := NewServer("test-inflight", "0")

	// Add a slow handler before starting
	s.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("done"))
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Start()
	}()

	time.Sleep(100 * time.Millisecond)
	addr := s.Addr()

	// Start a slow request
	slowDone := make(chan struct{})
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			t.Logf("slow request error: %v", err)
			close(slowDone)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("slow request got status %d, expected 200", resp.StatusCode)
		}
		close(slowDone)
	}()

	// Give the slow request a moment to start
	time.Sleep(100 * time.Millisecond)

	// Trigger shutdown while slow request is in flight
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.httpServer.Shutdown(ctx)

	// The slow request should still complete
	select {
	case <-slowDone:
		// Good: in-flight request completed
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request was not completed during graceful shutdown")
	}

	wg.Wait()
}
