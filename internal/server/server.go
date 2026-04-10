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
}

// Server wraps an http.Server with go-choir service configuration.
type Server struct {
	serviceName string
	httpServer  *http.Server
	mux         *http.ServeMux
	addr        string
	listener    net.Listener
	once        sync.Once
	done        chan struct{}
}

// NewServer creates a new Server for the given service name and port.
// The port should be a string like "8081". Use PortFromEnv to resolve
// the port from an environment variable with a default.
func NewServer(serviceName, port string) *Server {
	mux := http.NewServeMux()
	s := &Server{
		serviceName: serviceName,
		mux:         mux,
		addr:        fmt.Sprintf(":%s", port),
		done:        make(chan struct{}),
	}
	mux.HandleFunc("/health", s.handler)
	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}
	return s
}

// HandleFunc registers a handler for the given pattern on the server's mux.
// This must be called before Start.
func (s *Server) HandleFunc(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// handler is the HTTP handler for the /health endpoint.
func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Service: s.serviceName,
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
