package main

import (
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	port := server.PortFromEnv("AUTH_PORT", "8081")
	s := server.NewServer("auth", port)
	s.Start()
}
