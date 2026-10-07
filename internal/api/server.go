package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/buildinfo"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/hostfs"
	"github.com/batuta-ai/console/internal/i18n"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
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
	mux.HandleFunc("PUT /v0/missions/{id}/binding", s.auth(s.bindMission))
	mux.HandleFunc("GET /v0/missions/{id}/watch", s.auth(s.watchMission))
	mux.HandleFunc("GET /v0/missions/{id}/plan", s.auth(s.planDetail))
	mux.HandleFunc("POST /v0/missions/{id}/plan/approvals", s.auth(s.approvePlan))
	mux.HandleFunc("GET /v0/missions/{id}/plan/approvals/{receipt}", s.auth(s.planApproval))
	mux.HandleFunc("POST /v0/missions/{id}/execution", s.auth(s.startExecution))
	mux.HandleFunc("GET /v0/missions/{id}/execution", s.auth(s.executionState))
	mux.HandleFunc("POST /v0/missions/{id}/execution/resume", s.auth(s.resumeExecution))
	mux.HandleFunc("GET /v0/accounts", s.auth(s.listAccounts))
	mux.HandleFunc("POST /v0/accounts", s.auth(s.createAccount))
	mux.HandleFunc("PATCH /v0/accounts/{id}", s.auth(s.renameAccount))
	mux.HandleFunc("POST /v0/accounts/{id}/connect", s.auth(s.connectAccount))
	mux.HandleFunc("POST /v0/accounts/{id}/login", s.auth(s.loginAccount))
	mux.HandleFunc("POST /v0/workspaces/{id}/init", s.auth(s.initWorkspace))
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
	pty := session.ObservePTY(r.Context())
	writeJSON(w, capabilityResponse{
		API: "v0", Product: "batuta-console", Lang: i18n.Lang(),
		Integrations: []string{"generic", "batuta"}, Adapters: adapters.IDs(),
		Features:   map[string]bool{"pty": pty.Available, "watch": true, "limits": true, "batuta_inject": true, "batuta_inject_default": true, "skills_catalog": true, "account_fallback": false, "folder_picker": true},
		Runtime:    runningRuntime(),
		Provenance: buildinfo.Current(),
		Matrix:     releaseMatrix{Platforms: []string{"darwin", "linux"}, Providers: []string{"codex", "claude"}, Qualification: domain.CapabilityFact{Status: "pending", Code: "release_qualification_pending"}},
		PTY:        pty, Providers: adapters.Capabilities(r.Context()),
	})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListWorkspaces()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	for i := range list {
		list[i], err = s.Svc.DecorateWorkspaceChecked(list[i])
		if err != nil {
			writeErr(w, 500, "store", err.Error())
			return
		}
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
		writeErr(w, referenceStatus(err), "workspace", err.Error())
		return
	}
	ws, err = s.Svc.DecorateWorkspaceChecked(ws)
	if err != nil {
		writeErr(w, 500, "store", err.Error())
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
	q, err := s.Svc.SaveSquadContext(r.Context(), q)
	if err != nil {
		writeErr(w, referenceStatus(err), "squad", err.Error())
		return
	}
	writeJSON(w, q)
}

func (s *Server) saveSquadID(w http.ResponseWriter, r *http.Request) {
	q, err := s.Svc.Store.GetSquad(r.PathValue("id"))
	if err != nil {
		status := 500
		if errors.Is(err, sql.ErrNoRows) {
			status = 404
		}
		writeErr(w, status, "not_found", err.Error())
		return
	}
	q, err = s.Svc.SaveSquadContext(r.Context(), q)
	if err != nil {
		writeErr(w, referenceStatus(err), "squad", err.Error())
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
		v := s.Svc.ValidateDraftContext(r.Context(), draft)
		writeJSON(w, map[string]any{"validation": v, "squad": draft})
		return
	}
	v, q, err := s.Svc.ValidateSquadContext(r.Context(), r.PathValue("id"))
	if err != nil {
		status := 500
		if errors.Is(err, sql.ErrNoRows) {
			status = 404
		}
		writeErr(w, status, "not_found", err.Error())
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
		Brief       string `json:"brief"`
		SquadID     string `json:"squad_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	m, err := s.Svc.CreateMission(in.WorkspaceID, in.Title, in.Brief, in.SquadID)
	if err != nil {
		writeErr(w, referenceStatus(err), "mission", err.Error())
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
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	jobs := make(chan int)
	var checks sync.WaitGroup
	for range min(4, len(list)) {
		checks.Go(func() {
			for i := range jobs {
				list[i] = s.Svc.DecorateAccountContext(ctx, list[i])
			}
		})
	}
	for i := range list {
		jobs <- i
	}
	close(jobs)
	checks.Wait()
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

func (s *Server) renameAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	a, err := s.Svc.RenameAccount(r.PathValue("id"), in.Label)
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeErr(w, 400, "account", err.Error())
		return
	}
	writeJSON(w, a)
}

func (s *Server) connectAccount(w http.ResponseWriter, r *http.Request) {
	a, err := s.Svc.ConnectAccountContext(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, "account", err.Error())
		return
	}
	writeJSON(w, s.Svc.DecorateAccountContext(r.Context(), a))
}

func (s *Server) loginAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Owner  string `json:"owner"`
		Client string `json:"client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err.Error() != "EOF" {
		writeErr(w, 400, "json", err.Error())
		return
	}
	sess, err := s.Svc.StartLoginContext(r.Context(), r.PathValue("id"), in.Owner, in.Client)
	if err != nil {
		if writeReadinessError(w, err) {
			return
		}
		writeErr(w, 400, "login", err.Error())
		return
	}
	writeJSON(w, sess)
}

func (s *Server) initWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		service.InitInput
		Brief  string `json:"brief"`
		Owner  string `json:"owner"`
		Client string `json:"client"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, 400, "json", err.Error())
		return
	}
	sess, err := s.Svc.StartInitContext(r.Context(), r.PathValue("id"), in.Brief, in.Owner, in.Client, in.InitInput)
	if err != nil {
		if writeReadinessError(w, err) {
			return
		}
		status := referenceStatus(err)
		if errors.Is(err, service.ErrInitConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, "init", err.Error())
		return
	}
	writeJSON(w, sess)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.Store.ListSessions()
	if err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
	out := make([]domain.Session, 0, len(list))
	for _, sess := range list {
		live, err := s.Svc.ReconcileSession(sess)
		if err != nil {
			writeErr(w, 500, "store", err.Error())
			return
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
	sess, err := s.Svc.StartSessionContext(r.Context(), in)
	if err != nil {
		if writeReadinessError(w, err) {
			return
		}
		writeErr(w, referenceStatus(err), "session", err.Error())
		return
	}
	writeJSON(w, sess)
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Svc.StopSession(r.PathValue("id"))
	if errors.Is(err, service.ErrSessionNotOwned) {
		writeErr(w, 409, "session_not_owned", err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	if err != nil {
		writeErr(w, 500, "stop_failed", err.Error())
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
	if errors.Is(err, os.ErrPermission) {
		writeErr(w, 403, "not_owner", err.Error())
		return
	}
	if errors.Is(err, session.ErrInvalidControl) {
		writeErr(w, 400, "invalid_control", err.Error())
		return
	}
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	if err := s.Svc.Store.PutSessionWithEvent(sess, "session.control", map[string]any{"id": sess.ID, "owner": sess.Owner}); err != nil {
		writeErr(w, 500, "store", err.Error())
		return
	}
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
	if r.URL.Query().Has("mission") {
		s.writeMissionWatch(w, r, r.URL.Query().Get("mission"))
		return
	}
	view, err := s.Svc.Watch(r.URL.Query().Get("workspace"), r.URL.Query().Get("delivery"))
	if err != nil {
		writeErr(w, 400, "watch", err.Error())
		return
	}
	writeJSON(w, view)
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

// A supplied cursor is never reset, even when it is ahead of the catalog.
// Without a cursor, only the most recent bounded snapshot is sent.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(r) {
		writeEventHTTPError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
		return
	}
	cursor, supplied, err := eventCursor(r.URL.RawQuery)
	if err != nil {
		writeEventHTTPError(w, http.StatusBadRequest, "cursor", "cursor must be one non-negative decimal sequence")
		return
	}
	var list []domain.Event
	if supplied {
		list, err = s.Svc.Store.EventsSinceContext(r.Context(), cursor, 200)
	} else {
		list, err = s.Svc.Store.EventsRecentContext(r.Context(), 200)
	}
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		writeEventHTTPError(w, http.StatusInternalServerError, "events", "event snapshot unavailable")
		return
	}
	snapshot, err := json.Marshal(map[string]any{"type": "snapshot", "events": list})
	if err != nil {
		writeEventHTTPError(w, http.StatusInternalServerError, "events", "event snapshot unavailable")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"127.0.0.1:*", "localhost:*"}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		_, _, err := c.Read(ctx)
		if err == nil {
			_ = c.Close(websocket.StatusPolicyViolation, "unexpected event socket message")
		} else if !ordinaryEventDisconnect(ctx, err) {
			slog.Warn("event socket read failed")
		}
	}()
	defer func() { cancel(); _ = c.CloseNow(); <-readDone }()
	if !writeEventSocket(ctx, c, snapshot) {
		return
	}
	if len(list) > 0 {
		cursor = list[len(list)-1].Seq
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		more, err := s.Svc.Store.EventsSinceContext(ctx, cursor, 50)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("event socket catalog read failed")
				_ = c.Close(websocket.StatusInternalError, "event read failed")
			}
			return
		}
		for _, e := range more {
			payload, err := json.Marshal(map[string]any{"type": "event", "event": e})
			if err != nil {
				slog.Warn("event socket encoding failed")
				_ = c.Close(websocket.StatusInternalError, "event encoding failed")
				return
			}
			if !writeEventSocket(ctx, c, payload) {
				return
			}
			cursor = e.Seq
		}
		if len(more) == 50 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func writeEventHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message}); err != nil {
		slog.Warn("event socket HTTP diagnostic write failed")
	}
}

func eventCursor(rawQuery string) (int64, bool, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, false, err
	}
	values, supplied := query["cursor"]
	if !supplied {
		return 0, false, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, true, errors.New("invalid cursor")
	}
	for _, digit := range values[0] {
		if digit < '0' || digit > '9' {
			return 0, true, errors.New("invalid cursor")
		}
	}
	cursor, err := strconv.ParseInt(values[0], 10, 64)
	return cursor, true, err
}

func writeEventSocket(ctx context.Context, c *websocket.Conn, payload []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Write(writeCtx, websocket.MessageText, payload); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || !ordinaryEventDisconnect(ctx, err) {
			slog.Warn("event socket write failed")
		} else {
			slog.Info("event socket write ended")
		}
		return false
	}
	return true
}

func ordinaryEventDisconnect(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	status := websocket.CloseStatus(err)
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == websocket.StatusNoStatusRcvd {
		return true
	}
	var networkError *net.OpError
	return errors.As(err, &networkError) && !networkError.Timeout()
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

func writeReadinessError(w http.ResponseWriter, err error) bool {
	code, ok := service.ReadinessFailure(err)
	if !ok && errors.Is(err, exec.ErrNotFound) {
		code, ok = "cli_missing", true
	}
	if !ok && errors.Is(err, adapters.ErrUnsupportedCommand) {
		code, ok = "adapter_unsupported", true
	}
	if !ok {
		return false
	}
	status := http.StatusConflict
	if code == "provider_unavailable" || code == "provider_response_invalid" || code == "provider_forbidden" || code == "check_cancelled" || code == "probe_unavailable" || code == "cli_probe_failed" {
		status = http.StatusServiceUnavailable
	}
	response := map[string]string{"error": code, "message": err.Error()}
	var failure service.ReadinessError
	if errors.As(err, &failure) && failure.AccountID != "" {
		response["account_id"] = failure.AccountID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
	return true
}

var _ = adapters.All

func referenceStatus(err error) int {
	if errors.Is(err, domain.ErrInvalidReference) {
		return http.StatusBadRequest
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	if service.CatalogReferenceFailure(err) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}
