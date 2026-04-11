package main

import (
	"github.com/yusefmosiah/go-choir/internal/sandbox"
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	cfg := sandbox.LoadConfig()

	s := server.NewServer("sandbox", cfg.Port)

	h := sandbox.NewHandler(cfg)
	sandbox.RegisterRoutes(s, h)

	s.Start()
}
