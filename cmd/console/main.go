package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/tui"
)

func main() {
	webOnly := flag.Bool("web", false, "print the local web URL and exit")
	home := flag.String("home", "", "data directory")
	listen := flag.String("listen", "", "loopback address")
	flag.Parse()
	paths, err := runtime.Resolve(*home, *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	token, err := runtime.LoadOrCreateAuth(paths.Auth)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	base := "http://" + paths.Listen
	api := client.New(base, token)
	if !api.Alive() {
		if err := startDaemon(paths); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if api.Alive() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !api.Alive() {
			fmt.Fprintln(os.Stderr, "consoled did not become ready")
			os.Exit(1)
		}
	}
	if *webOnly {
		fmt.Printf("%s/?token=%s\n", base, token)
		return
	}
	if err := tui.Run(api); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func startDaemon(paths runtime.Paths) error {
	bin, err := exec.LookPath("consoled")
	if err != nil {
		self, err2 := os.Executable()
		if err2 != nil {
			return err
		}
		cand := filepath.Join(filepath.Dir(self), "consoled")
		if _, err3 := os.Stat(cand); err3 != nil {
			return err
		}
		bin = cand
	}
	cmd := exec.Command(bin, "-home", paths.Home, "-listen", paths.Listen)
	logFile, err := os.OpenFile(filepath.Join(paths.Home, "consoled.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	configureDaemon(cmd, logFile)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	go func() { _ = cmd.Wait() }()
	return nil
}
