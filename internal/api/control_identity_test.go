//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/coder/websocket"
)

func TestControlRelinquishHTTP(t *testing.T) {
	h, svc := testServer(t)
	s, err := svc.Session.Start(domain.Session{ID: "selected", Argv: []string{"/bin/cat"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Session.Stop(s.ID)
	if err := svc.Store.PutSession(s); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/v0/sessions/selected/control", map[string]string{"surface": "web", "client": "browser-one"}, 200)
	post(t, h, "/v0/sessions/selected/control", map[string]string{"surface": "none", "client": "terminal-two"}, 403)
	released := post(t, h, "/v0/sessions/selected/control", map[string]string{"surface": "none", "client": "browser-one"}, 200)
	if released["owner"] != "none" || (released["owner_client"] != nil && released["owner_client"] != "") {
		t.Fatalf("invented recipient: %v", released)
	}
	stored, err := svc.Store.GetSession(s.ID)
	if err != nil || stored.Owner != "none" || stored.OwnerClient != "" {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	post(t, h, "/v0/sessions/selected/control", map[string]string{"surface": "tui", "client": "terminal-two"}, 200)
	post(t, h, "/v0/sessions/selected/control", map[string]string{"surface": "web"}, 400)
}

func TestIndependentTerminalClientsControlSelectedPTY(t *testing.T) {
	h, svc := testServer(t)
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range []string{"selected", "untouched"} {
		s, err := svc.Session.Start(domain.Session{ID: id, Argv: []string{"/bin/sh", "-c", `stty -echo; printf 'READY\n'; while read line; do stty size; printf 'ACK:%s\n' "$line"; done`}}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Session.Stop(id)
		if err := svc.Store.PutSession(s); err != nil {
			t.Fatal(err)
		}
	}
	type client struct {
		id     string
		socket *websocket.Conn
		output chan string
		http   *http.Client
	}
	attach := func(id string) *client {
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v0/sessions/selected/terminal?token=test-token&client="+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		result := &client{id: id, socket: c, output: make(chan string, 64), http: &http.Client{}}
		go func() {
			defer close(result.output)
			for {
				_, b, err := c.Read(ctx)
				if err != nil {
					return
				}
				select {
				case result.output <- string(b):
				case <-ctx.Done():
					return
				}
			}
		}()
		return result
	}
	first, second := attach("browser-one"), attach("terminal-two")
	defer first.socket.Close(websocket.StatusNormalClosure, "")
	defer second.socket.Close(websocket.StatusNormalClosure, "")
	await := func(c *client, want string) string {
		t.Helper()
		var output string
		for !strings.Contains(output, want) {
			select {
			case b, ok := <-c.output:
				if !ok {
					t.Fatalf("%s closed: %s", c.id, output)
				}
				output += b
			case <-ctx.Done():
				t.Fatalf("%s missing %q: %s", c.id, want, output)
			}
		}
		return output
	}
	control := func(c *client, surface string, status int) {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"surface": surface, "client": c.id})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v0/sessions/selected/control", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer test-token")
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != status {
			t.Fatalf("%s control %s: %d %s", c.id, surface, res.StatusCode, body)
		}
	}
	send := func(c *client, data string) {
		t.Helper()
		if err := c.socket.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	await(first, "READY")
	await(second, "READY")
	control(first, "web", 200)
	send(first, `{"type":"resize","cols":97,"rows":31}`)
	send(first, "first\n")
	for _, c := range []*client{first, second} {
		output := await(c, "ACK:first")
		if !strings.Contains(output, "31 97") {
			t.Fatalf("resize missing: %s", output)
		}
	}
	control(second, "none", 403)
	control(first, "none", 200)
	send(first, `{"type":"resize","cols":12,"rows":12}`)
	send(first, "forbidden\n")
	if err := first.socket.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ := svc.Session.Snapshot("selected")
	if snap.Owner != "none" || snap.OwnerClient != "" || snap.Cols != 97 || snap.Rows != 31 {
		t.Fatalf("released client retained authority: %+v", snap)
	}
	control(second, "tui", 200)
	send(second, `{"type":"resize","cols":88,"rows":29}`)
	send(second, "second\n")
	for _, c := range []*client{first, second} {
		output := await(c, "ACK:second")
		if strings.Contains(output, "forbidden") || !strings.Contains(output, "29 88") {
			t.Fatalf("wrong input/resize: %s", output)
		}
	}
	send(first, "former-owner\n")
	if err := first.socket.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	send(second, "barrier\n")
	for _, c := range []*client{first, second} {
		if output := await(c, "ACK:barrier"); strings.Contains(output, "former-owner") {
			t.Fatalf("former owner wrote: %s", output)
		}
	}
	untouched, _ := svc.Session.Snapshot("untouched")
	if untouched.Owner != "none" || untouched.OwnerClient != "" || untouched.Cols != 110 {
		t.Fatalf("wrong session changed: %+v", untouched)
	}
}

func TestUnsavedSquadValidationDoesNotCreateIdentity(t *testing.T) {
	h, svc := testServer(t)
	before, err := svc.Store.ListSquads()
	if err != nil {
		t.Fatal(err)
	}
	result := post(t, h, "/v0/squads/draft/validate", map[string]any{"name": "Unsaved draft", "members": []any{}}, 200)
	if result["validation"] == nil {
		t.Fatalf("missing validation: %v", result)
	}
	after, err := svc.Store.ListSquads()
	if err != nil || len(after) != len(before) {
		t.Fatalf("draft validation persisted a squad: %v %v", after, err)
	}
}
