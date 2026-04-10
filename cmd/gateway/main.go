package main

import (
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	port := server.PortFromEnv("GATEWAY_PORT", "8084")
	s := server.NewServer("gateway", port)
	s.Start()
}
