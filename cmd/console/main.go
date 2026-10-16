package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"

	"github.com/batuta-ai/console/internal/buildinfo"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/tui"
)

func main() {
	version := flag.Bool("version", false, "print binary version, revision, actual Go toolchain and provenance as JSON")
	webOnly := flag.Bool("web", false, "open the local web UI and print its URL")
	home := flag.String("home", "", "data directory")
	listen := flag.String("listen", "", "loopback address")
	flag.Parse()
	if *version {
		if err := buildinfo.WriteDiagnostic(os.Stdout, "console"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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
	if err := ensureDaemon(api, paths); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *webOnly {
		url := fmt.Sprintf("%s/?token=%s", base, token)
		fmt.Println(url)
		if err := openBrowser(url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
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
		name := "consoled"
		if stdruntime.GOOS == "windows" {
			name += ".exe"
		}
		cand := filepath.Join(filepath.Dir(self), name)
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
