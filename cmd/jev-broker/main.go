package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jevbroker/internal/broker"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: jev-broker serve")
		os.Exit(2)
	}
	if err := run(); err != nil {
		// Do not print provider errors, request content, environment or secrets.
		fmt.Fprintln(os.Stderr, "jev-broker: startup or server failure")
		os.Exit(1)
	}
}

func run() error {
	config, err := broker.ConfigFromEnv()
	if err != nil {
		return err
	}
	registry, err := broker.LoadRegistry(config.Clients)
	if err != nil {
		return err
	}
	audit, err := broker.OpenAudit(config.Audit)
	if err != nil {
		return err
	}
	provider, err := broker.NewOpenRouterProvider(config.APIKey)
	if err != nil {
		return err
	}
	service, err := broker.NewService(provider, audit, config.Model)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.Addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: broker.Handler(registry, service), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
