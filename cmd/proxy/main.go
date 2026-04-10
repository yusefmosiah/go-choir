package main

import (
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	port := server.PortFromEnv("PROXY_PORT", "8082")
	s := server.NewServer("proxy", port)
	s.Start()
}
