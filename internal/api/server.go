package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/hostfs"
	"github.com/batuta-ai/console/internal/service"
	"github.com/coder/websocket"
)

type Server struct {
	Svc           *service.Service
	Token         string
	Web           fs.FS
	Origin        string
	PickDirectory func(ctx context.Context) (path string, canceled bool, err error)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /v0/capabilities", s.auth(s.capabilities))
	mux.HandleFunc("GET /v0/workspaces", s.auth(s.listWorkspaces))
	mux.HandleFunc("POST /v0/workspaces", s.auth(s.createWorkspace))
	mux.HandleFunc("POST /v0/workspaces/pick-root", s.auth(s.pickWorkspaceRoot))
	mux.HandleFunc("GET /v0/squads", s.auth(s.listSquads))
	mux.HandleFunc("POST /v0/squads", s.auth(s.saveSquad))
	mux.HandleFunc("GET /v0/skills", s.auth(s.listSkills))
	mux.HandleFunc("POST /v0/squads/{id}/validate", s.auth(s.validateSquad))
	mux.HandleFunc("POST /v0/squads/{id}/save", s.auth(s.saveSquadID))
	mux.HandleFunc("GET /v0/missions", s.auth(s.listMissions))
	mux.HandleFunc("POST /v0/missions", s.auth(s.createMission))
	mux.HandleFunc("GET /v0/accounts", s.auth(s.listAccounts))
	mux.HandleFunc("POST /v0/accounts", s.auth(s.createAccount))
	mux.HandleFunc("POST /v0/accounts/{id}/connect", s.auth(s.connectAccount))
	mux.HandleFunc("GET /v0/sessions", s.auth(s.listSessions))
	mux.HandleFunc("POST /v0/sessions", s.auth(s.startSession))
	mux.HandleFunc("POST /v0/sessions/{id}/stop", s.auth(s.stopSession))
	mux.HandleFunc("POST /v0/sessions/{id}/control", s.auth(s.control))
	mux.HandleFunc("GET /v0/sessions/{id}/terminal", s.terminal)
	mux.HandleFunc("GET /v0/watch", s.auth(s.watch))
	mux.HandleFunc("POST /v0/watch/answer", s.auth(s.answer))
	mux.HandleFunc("GET /v0/limits", s.auth(s.limits))
	mux.HandleFunc("GET /v0/activity", s.auth(s.activity))
	mux.HandleFunc("GET /v0/events", s.events)
	if s.Web != nil {
		mux.Handle("/", http.FileServer(http.FS(s.Web)))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>Batuta Console</title><p>consoled API v0 · build web/ to embed the UI</p>")
		})
	}
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkToken(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
			return
		}
		next(w, r)
	}
}

func (s *Server) checkToken(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" && (strings.HasSuffix(r.URL.Path, "/terminal") || r.URL.Path == "/v0/events") {
		got = r.URL.Query().Get("token")
	}
	return got != "" && got == s.Token
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"api": "v0", "product": "batuta-console",
		"integrations": []string{"generic", "batuta"},
		"adapters":     []string{"codex", "claude", "opencode", "gemini"},
		"features":     map[string]any{"pty": true, "watch": true, "limits": true, "batuta_inject": true, "batuta_inject_default": true, "skills_catalog": true, "account_fallback": false, "folder_picker": true},
	})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListWorkspaces()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) pickWorkspaceRoot(w http.ResponseWriter, r *http.Request) {
	pick := s.PickDirectory
	if pick == nil {
		pick = hostfs.PickDirectory
	}
	path, canceled, err := pick(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "picker", err.Error())
		return
	}
	if canceled || path == "" {
		writeJSON(w, map[string]any{"canceled": true})
		return
	}
	writeJSON(w, map[string]any{"path": path})
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Root string `json:"root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	ws, err := s.Svc.CreateWorkspace(in.Name, in.Root)
	if err != nil {
		writeErr(w, 400, "workspace", err.Error())
		return
	}
	writeJSON(w, ws)
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"root":           batuta.SkillsRoot(),
		"inject_default": true,
		"skills":         batuta.ListSkills(),
	})
}

func (s *Server) listSquads(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListSquads()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) saveSquad(w http.ResponseWriter, r *http.Request) {
	var q domain.Squad
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	q, err := s.Svc.SaveSquad(q)
	if err != nil {
		writeErr(w, 400, "squad", err.Error())
		return
	}
	writeJSON(w, q)
}

func (s *Server) saveSquadID(w http.ResponseWriter, r *http.Request) {
	q, err := s.Svc.Store.GetSquad(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	q, err = s.Svc.SaveSquad(q)
	if err != nil {
		writeErr(w, 400, "squad", err.Error())
		return
	}
	writeJSON(w, q)
}

func (s *Server) validateSquad(w http.ResponseWriter, r *http.Request) {
	var draft domain.Squad
	_ = json.NewDecoder(r.Body).Decode(&draft)
	if draft.Name != "" || len(draft.Members) > 0 {
		if draft.ID == "" {
			draft.ID = r.PathValue("id")
		}
		v := s.Svc.ValidateDraft(draft)
		writeJSON(w, map[string]any{"validation": v, "squad": draft})
		return
	}
	v, q, err := s.Svc.ValidateSquad(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	writeJSON(w, map[string]any{"validation": v, "squad": q})
}

func (s *Server) listMissions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListMissions(r.URL.Query().Get("workspace"))
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) createMission(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		Title       string `json:"title"`
		SquadID     string `json:"squad_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	m, err := s.Svc.CreateMission(in.WorkspaceID, in.Title, in.SquadID)
	if err != nil {
		writeErr(w, 400, "mission", err.Error())
		return
	}
	writeJSON(w, m)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListAccounts()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label   string `json:"label"`
		Adapter string `json:"adapter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	a, err := s.Svc.CreateAccount(in.Label, in.Adapter)
	if err != nil {
		writeErr(w, 400, "account", err.Error())
		return
	}
	writeJSON(w, a)
}

func (s *Server) connectAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.Svc.ConnectAccount(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, "account", err.Error())
		return
	}
	writeJSON(w, a)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListSessions()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	out := make([]domain.Session, 0, len(list))
	for _, sess := range list {
		live := s.Svc.ReconcileSession(sess)
		if live.Process != sess.Process {
			_ = s.Svc.Store.PutSession(live)
		}
		out = append(out, live)
	}
	writeJSON(w, out)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	var in service.StartSessionIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	sess, err := s.Svc.StartSession(in)
	if err != nil {
		writeErr(w, 400, "session", err.Error())
		return
	}
	writeJSON(w, sess)
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Svc.StopSession(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	writeJSON(w, sess)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Surface string `json:"surface"`
		Client  string `json:"client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	sess, err := s.Svc.Session.Take(r.PathValue("id"), in.Surface, in.Client)
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	_ = s.Svc.Store.PutSession(sess)
	_, _ = s.Svc.Store.AppendEvent("session.control", map[string]any{"id": sess.ID, "owner": sess.Owner})
	writeJSON(w, sess)
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(r) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
		return
	}
	id := r.PathValue("id")
	client := r.URL.Query().Get("client")
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"127.0.0.1:*", "localhost:*"}})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	outCh := make(chan []byte, 32)
	unsub, err := s.Svc.Session.Subscribe(id, func(b []byte) {
		select {
		case outCh <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		return
	}
	defer unsub()
	go func() {
		for {
			select {
			case <-r.Context().Done():
				return
			case b, ok := <-outCh:
				if !ok {
					return
				}
				if err := c.Write(r.Context(), websocket.MessageBinary, b); err != nil {
					return
				}
			}
		}
	}()
	for {
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) == nil {
			if msg["type"] == "resize" {
				cols, _ := msg["cols"].(float64)
				rows, _ := msg["rows"].(float64)
				_ = s.Svc.Session.Resize(id, client, int(cols), int(rows))
				continue
			}
		}
		if err := s.Svc.Session.Input(id, client, data); err != nil && !errors.Is(err, os.ErrPermission) {
			return
		}
	}
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	view, err := s.Svc.Watch(r.URL.Query().Get("workspace"), r.URL.Query().Get("delivery"))
	if err != nil {
		writeErr(w, 400, "watch", err.Error())
		return
	}
	writeJSON(w, view)
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var req batuta.AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	id, err := s.Svc.Answer(req)
	if err != nil {
		writeErr(w, 400, "answer", err.Error())
		return
	}
	writeJSON(w, map[string]any{"delivery": id})
}

func (s *Server) limits(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Limits()
	if err != nil {
		writeErr(w, 500, "limits", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.EventsRecent(100)
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(r) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"127.0.0.1:*", "localhost:*"}})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	list, _ := s.Svc.Store.EventsSince(0, 200)
	b, _ := json.Marshal(map[string]any{"type": "snapshot", "events": list})
	_ = c.Write(r.Context(), websocket.MessageText, b)
	cursor := int64(0)
	if len(list) > 0 {
		cursor = list[len(list)-1].Seq
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			more, err := s.Svc.Store.EventsSince(cursor, 50)
			if err != nil || len(more) == 0 {
				continue
			}
			for _, e := range more {
				payload, _ := json.Marshal(map[string]any{"type": "event", "event": e})
				if err := c.Write(r.Context(), websocket.MessageText, payload); err != nil {
					return
				}
				cursor = e.Seq
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, codeName, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": codeName, "message": msg})
}

var _ = adapters.All
