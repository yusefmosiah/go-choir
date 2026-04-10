package main

import (
	"log"

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

	s := server.NewServer("auth", cfg.Port)
	s.Start()
}
