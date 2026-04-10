package main

import (
	"log"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/yusefmosiah/go-choir/internal/auth"
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	cfg, err := auth.LoadConfig()
	if err != nil {
		log.Fatalf("auth config: %v", err)
	}

	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("auth dirs: %v", err)
	}

	store, err := auth.OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("auth store: %v", err)
	}
	defer store.Close()

	// Create the WebAuthn Relying Party instance bound to the configured RP ID.
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "go-choir",
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		log.Fatalf("auth webauthn: %v", err)
	}

	// Load the Ed25519 private key for JWT signing.
	signer, err := auth.LoadPrivateKey(cfg.JWTPrivateKeyPath)
	if err != nil {
		log.Fatalf("auth signing key: %v", err)
	}

	// Create the auth handler with store, WebAuthn, config, and signer.
	handler := auth.NewHandler(store, wa, cfg, signer)

	s := server.NewServer("auth", cfg.Port)

	// Register /auth/* routes.
	s.HandleFunc("/auth/register/begin", handler.HandleRegisterBegin)
	s.HandleFunc("/auth/register/finish", handler.HandleRegisterFinish)
	s.HandleFunc("/auth/login/begin", handler.HandleLoginBegin)
	s.HandleFunc("/auth/login/finish", handler.HandleLoginFinish)
	s.HandleFunc("/auth/session", handler.HandleSession)

	s.Start()
}
