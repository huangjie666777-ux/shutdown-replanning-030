package main

import (
	"context"
	"errors"
	"flag"
	"github.com/huangjie666777-ux/shutdown-scheduler-025/internal/server"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()
	srv := &http.Server{Addr: *addr, Handler: server.NewRouter(), ReadHeaderTimeout: 5 * time.Second}
	stopped, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-stopped.Done()
		ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("listening on http://%s", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
