package main

import (
	"context"
	"errors"
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
	"github.com/batuta-ai/console/internal/buildinfo"
	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
)

func main() {
	if handled, err := nativeExecutorMode(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	version := flag.Bool("version", false, "print binary version, revision, actual Go toolchain and provenance as JSON")
	listen := flag.String("listen", "", "loopback address")
	home := flag.String("home", "", "data directory")
	core := flag.String("core", "", "Core CLI matching the qualified SDK (default: batuta on PATH)")
	flag.Parse()
	if *version {
		if err := buildinfo.WriteDiagnostic(os.Stdout, "consoled"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	paths, err := runtime.Resolve(*home, *listen)
	if err != nil {
		log.Fatal(err)
	}
	if err := runDaemon(paths, daemonOptions{CoreExecutable: *core}); err != nil {
		log.Fatal(err)
	}
}

func runDaemon(paths runtime.Paths, options ...daemonOptions) (err error) {
	if len(options) > 1 {
		return errors.New("daemon accepts one option set")
	}
	token, err := runtime.LoadOrCreateAuth(paths.Auth)
	if err != nil {
		return err
	}
	lock, err := runtime.WriteLock(paths.Lock, runtime.LockInfo{PID: os.Getpid(), Addr: paths.Listen, Home: paths.Home})
	if err != nil {
		return err
	}
	defer lock.Close()
	st, err := store.Open(paths.DB)
	if err != nil {
		return err
	}
	canCloseStore := true
	defer func() {
		// On timeout main exits with failure. Leave the catalog open until process
		// exit rather than racing callbacks or handlers still using it.
		if canCloseStore {
			err = errors.Join(err, st.Close())
		}
	}()
	svc := service.New(st, session.New(paths.Logs), limits.New(limits.Disk{Home: paths.Home}))
	svc.Home = paths.Home
	if len(options) == 1 {
		svc.CoreExecutable = options[0].CoreExecutable
	}
	if err := svc.ReconcileSessions(); err != nil {
		return err
	}
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
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	fmt.Fprintf(os.Stderr, "consoled listening on http://%s\n", ln.Addr())
	select {
	case err = <-served:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case <-ctx.Done():
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drainErr := drainExecution(drainCtx, svc)
	httpErr := srv.Shutdown(drainCtx)
	canCloseStore = !errors.Is(drainErr, context.DeadlineExceeded) && !errors.Is(drainErr, context.Canceled) && httpErr == nil
	if !canCloseStore {
		err = errors.Join(err, fmt.Errorf("daemon shutdown incomplete: %w", errors.Join(drainErr, httpErr)), srv.Close())
	} else {
		err = errors.Join(err, drainErr)
	}
	return err
}
