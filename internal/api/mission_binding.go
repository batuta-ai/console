package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
)

func (s *Server) bindMission(w http.ResponseWriter, r *http.Request) {
	var req service.MissionBindingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeErr(w, 400, "binding_request", "invalid binding request")
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writeErr(w, 400, "binding_request", "invalid binding request")
		return
	}
	m, err := s.Svc.BindMissionContext(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeBindingObservationError(w, r, err)
		return
	}
	writeJSON(w, m)
}

func (s *Server) watchMission(w http.ResponseWriter, r *http.Request) {
	s.writeMissionWatch(w, r, r.PathValue("id"))
}
func (s *Server) writeMissionWatch(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		writeBindingError(w, domain.ErrInvalidReference)
		return
	}
	if mission := r.URL.Query().Get("mission"); mission != "" && mission != id {
		writeBindingError(w, domain.ErrInvalidReference)
		return
	}
	view, err := s.Svc.WatchMissionContext(r.Context(), id, r.URL.Query().Get("workspace"), r.URL.Query().Get("delivery"))
	if err != nil {
		writeBindingObservationError(w, r, err)
		return
	}
	writeJSON(w, view)
}

func writeBindingObservationError(w http.ResponseWriter, r *http.Request, err error) {
	if cause := context.Cause(r.Context()); cause != nil && errors.Is(err, cause) {
		err = errors.Join(r.Context().Err(), err)
	}
	writeBindingError(w, err)
}

func writeBindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeErr(w, 408, "binding_canceled", "binding observation canceled")
	case errors.Is(err, domain.ErrInvalidReference):
		writeErr(w, 400, "binding_reference", "invalid mission, workspace or artifact reference")
	case errors.Is(err, domain.ErrBindingConflict):
		writeErr(w, 409, "binding_conflict", "mission or delivery already bound")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, batuta.ErrArtifactMissing):
		writeErr(w, 404, "binding_missing", "mission, workspace or artifact unavailable")
	case errors.Is(err, batuta.ErrArtifactInvalid):
		writeErr(w, 422, "binding_artifact", "artifact identity or format is invalid")
	case errors.Is(err, batuta.ErrArtifactIO):
		writeErr(w, 503, "binding_io", "artifact temporarily unreadable")
	default:
		writeErr(w, 500, "binding_store", "catalog operation failed")
	}
}
