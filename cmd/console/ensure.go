package main

import (
	"fmt"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/runtime"
)

func mismatchErr(listen, home string) error {
	return fmt.Errorf("consoled is already listening on http://%s but this auth secret does not match.\nStop that process or pass -home to its data directory (this client used %s)", listen, home)
}

func ensureDaemon(api *client.Client, paths runtime.Paths) error {
	switch api.Probe() {
	case client.StatusReady:
		return nil
	case client.StatusUnauthorized:
		return mismatchErr(paths.Listen, paths.Home)
	}
	if err := startDaemon(paths); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		switch api.Probe() {
		case client.StatusReady:
			return nil
		case client.StatusUnauthorized:
			return mismatchErr(paths.Listen, paths.Home)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("consoled did not become ready")
}
