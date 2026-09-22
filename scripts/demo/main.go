// The demo is a developer tool. It is never included in the gateway executable.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := flag.Int("port", 18084, "loopback dashboard port; 0 chooses an available port")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *port); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, port int) error {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	demo, err := newDemo(ctx, origin)
	if err != nil {
		return err
	}
	defer demo.close()
	httpServer := &http.Server{Handler: demo.handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	fmt.Printf("Pocket AI Gateway DEMO — synthetic data, local mock provider\nDashboard: %s/_/\nEmail: %s\nPassword: %s\nTemporary data is deleted when you stop this process (Ctrl+C).\n", origin, demo.owner.Email, demo.password)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = httpServer.Shutdown(shutdown)
	if err != nil {
		_ = httpServer.Close()
	}
	serveErr := <-done
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(err, serveErr)
}
