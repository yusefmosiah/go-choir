package main

import (
	"github.com/yusefmosiah/go-choir/internal/server"
)

func main() {
	port := server.PortFromEnv("VMCTL_PORT", "8083")
	s := server.NewServer("vmctl", port)
	s.Start()
}
