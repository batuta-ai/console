package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

func TestDaemonDrainClosesNativeAndInteractiveAdmission(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, session.New(t.TempDir()), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := drainExecution(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session.ReserveNative(map[string]int{"selected": 1}); !errors.Is(err, session.ErrProbeClosing) {
		t.Fatal("daemon drain left native account admission open", err)
	}
	_, err = svc.Session.StartPending(domain.Session{ID: "closing", Argv: []string{"missing"}}, nil, "", 0)
	if err == nil {
		t.Fatal("daemon drain left interactive admission open")
	}
}
