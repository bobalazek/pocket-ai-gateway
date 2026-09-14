package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bobalazek/pocket-ai-gateway/internal/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(app.Execute(ctx, version, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
