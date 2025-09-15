package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Bay0312/cafelog/internal/broker"
	"github.com/Bay0312/cafelog/pkg/api"
)

func main() {
	httpAddr := ":8080"
	tcpAddr := ":7070"

	// Start HTTP (/healthz, /metrics)
	httpSrv := api.StartHTTP(httpAddr)
	log.Printf("HTTP listening on %s", httpAddr)

	// Start TCP broker
	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		log.Fatalf("listen TCP: %v", err)
	}
	b := broker.New()
	go func() {
		if err := b.ServeTCP(ln); err != nil {
			log.Fatalf("broker stopped: %v", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	_ = ln.Close()
}
