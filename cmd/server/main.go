package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tomaslejdung/gopeep/pkg/signal"
)

func main() {
	port := flag.Int("port", 8080, "Server port")
	flag.Parse()

	// Check for PORT env var (for cloud deployments)
	if envPort := os.Getenv("PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", port)
	}

	server := signal.NewServer()

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("GoPeep Signal Server starting on %s", addr)
	log.Printf("Share URL format: https://your-domain/%s", signal.GenerateRoomCode())

	if err := server.StartServer(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
