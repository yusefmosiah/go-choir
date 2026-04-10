package main

import (
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	port := server.PortFromEnv("SANDBOX_PORT", "8085")
	s := server.NewServer("sandbox", port)
	s.Start()
}
