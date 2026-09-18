package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
)

func main() {
	listen := flag.String("listen", "", "loopback address")
	home := flag.String("home", "", "data directory")
	flag.Parse()
	paths, err := runtime.Resolve(*home, *listen)
	if err != nil {
		log.Fatal(err)
	}
	token, err := runtime.LoadOrCreateAuth(paths.Auth)
	if err != nil {
		log.Fatal(err)
	}
	lock, err := runtime.WriteLock(paths.Lock, runtime.LockInfo{PID: os.Getpid(), Addr: paths.Listen, Home: paths.Home})
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()
	st, err := store.Open(paths.DB)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, session.New(paths.Logs), limits.New(limits.Disk{Home: paths.Home}))
	svc.Home = paths.Home
	if err := svc.EnsureBatutaCatalog(); err != nil {
		log.Printf("batuta catalog: %v", err)
	}
	web := webFiles()
	srv := &http.Server{
		Addr:              paths.Listen,
		Handler:           (&api.Server{Svc: svc, Token: token, Web: web}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", paths.Listen)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	fmt.Fprintf(os.Stderr, "consoled listening on http://%s\n", paths.Listen)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
