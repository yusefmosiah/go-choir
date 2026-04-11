// Package server provides a shared HTTP server for go-choir services.
// It configures a health endpoint, graceful shutdown, and port configuration
// via environment variables.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// healthResponse is the JSON structure returned by the /health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Addr    string `json:"addr,omitempty"`
}

// Server wraps an http.Server with go-choir service configuration.
type Server struct {
	serviceName  string
	httpServer   *http.Server
	mux          *http.ServeMux
	addr         string
	listener     net.Listener
	once         sync.Once
	done         chan struct{}
	healthHandler http.HandlerFunc
}

// defaultBindHost is the default host address that services bind to.
// Binding to 127.0.0.1 (localhost only) ensures that internal service
// ports are not reachable from external networks, even if the host
// firewall is misconfigured. This is a defense-in-depth measure for
// VAL-DEPLOY-007: only the Caddy edge (ports 80/443) should be
// internet-reachable on Node B.
const defaultBindHost = "127.0.0.1"

// NewServer creates a new Server for the given service name and port.
// The port should be a string like "8081". Use PortFromEnv to resolve
// the port from an environment variable with a default.
//
// By default, the server binds to 127.0.0.1 (localhost only) so that
// internal service ports are not externally reachable. Set the
// SERVER_HOST environment variable to override the bind host (e.g.
// SERVER_HOST=0.0.0.0 to listen on all interfaces).
//
// A default /health endpoint is registered automatically. Services that
// need a custom health handler (e.g., to report upstream reachability)
// should call SetHealthHandler before Start.
func NewServer(serviceName, port string) *Server {
	mux := http.NewServeMux()
	bindHost := BindHostFromEnv()
	addr := fmt.Sprintf("%s:%s", bindHost, port)
	s := &Server{
		serviceName:   serviceName,
		mux:           mux,
		addr:          addr,
		done:          make(chan struct{}),
		healthHandler: nil, // set below after method receiver is available
	}
	// Set the default health handler and register the /health route.
	s.healthHandler = s.defaultHealthHandler
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		s.healthHandler(w, r)
	})
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return s
}

// SetHealthHandler replaces the default /health handler with a custom
// one. This must be called before Start. It allows services like the
// proxy to report upstream reachability or other custom health state
// instead of the generic "ok" response.
func (s *Server) SetHealthHandler(handler http.HandlerFunc) {
	s.healthHandler = handler
}

// HandleFunc registers a handler for the given pattern on the server's mux.
// This must be called before Start.
func (s *Server) HandleFunc(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// defaultHealthHandler is the default HTTP handler for the /health endpoint.
// It returns a simple "ok" status with the service name and listening address.
func (s *Server) defaultHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Service: s.serviceName,
		Addr:    s.Addr(),
	})
}

// Addr returns the address the server is listening on, in the form "host:port".
// Returns an empty string if the server hasn't started yet.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Start starts the HTTP server and blocks until the server is shut down.
// It listens for SIGTERM and SIGINT signals and performs graceful shutdown.
func (s *Server) Start() {
	// Set up signal channel for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-quit
		log.Printf("%s: received %s, shutting down gracefully", s.serviceName, sig)
		s.Shutdown()
	}()

	log.Printf("%s: starting server on %s", s.serviceName, s.addr)

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Fatalf("%s: failed to listen on %s: %v", s.serviceName, s.addr, err)
	}
	s.listener = ln

	if err := s.httpServer.Serve(ln); err != http.ErrServerClosed {
		log.Fatalf("%s: server error: %v", s.serviceName, err)
	}

	s.once.Do(func() { close(s.done) })
}

// Shutdown gracefully shuts down the server with a 10-second timeout.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Printf("%s: shutdown error: %v", s.serviceName, err)
	}
	s.once.Do(func() { close(s.done) })
	log.Printf("%s: server stopped", s.serviceName)
}

// PortFromEnv reads a port from the given environment variable, returning
// the defaultPort if the variable is unset or empty.
func PortFromEnv(envVar, defaultPort string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultPort
}

// BindHostFromEnv reads the bind host from the SERVER_HOST environment
// variable, returning "127.0.0.1" if the variable is unset or empty.
// This controls which network interface the server listens on.
func BindHostFromEnv() string {
	if v := os.Getenv("SERVER_HOST"); v != "" {
		return v
	}
	return defaultBindHost
}
