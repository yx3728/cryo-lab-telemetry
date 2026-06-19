package api

import (
	"net/http"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

// handleChannels lists the channels that currently have data, with their latest
// value/timestamp. Public read — the dashboard uses it to build its chart list
// without hard-coding which channels exist.
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.Channels(r.Context())
	if err != nil {
		s.log.Error("channels query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if channels == nil {
		channels = []store.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}
