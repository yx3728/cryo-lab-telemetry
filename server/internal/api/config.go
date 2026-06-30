package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

const (
	samplingIntervalKey = "sampling_interval_seconds"
	maxEmailsKey        = "alert_max_emails_per_day"
)

// --- login (control plane) ---------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// handleLogin verifies admin credentials and issues a JWT. The error message is
// identical for unknown user and wrong password, so neither is distinguishable.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.jwt.CheckCredentials(req.Username, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, expires, err := s.jwt.Issue(req.Username)
	if err != nil {
		s.log.Error("login: issue token failed", "err", err)
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: expires.UTC().Format("2006-01-02T15:04:05Z07:00")})
}

// --- config (read public, write admin) ---------------------------------------

// configPayload is both the GET response and the PUT request body. On PUT, a
// zero SamplingIntervalSeconds means "leave unchanged"; Thresholds are upserted.
type configPayload struct {
	SamplingIntervalSeconds int               `json:"sampling_interval_seconds"`
	AlertMaxEmailsPerDay    int               `json:"alert_max_emails_per_day"`
	Thresholds              []store.Threshold `json:"thresholds"`
}

// handleGetConfig returns the current sampling interval and thresholds. Public
// read: the collector polls this to close the configuration loop, and the admin
// UI reads it to populate the editor.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	payload, err := s.currentConfig(r)
	if err != nil {
		s.log.Error("config: read failed", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read config")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// handlePutConfig updates the sampling interval and/or thresholds. Admin only.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req configPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.SamplingIntervalSeconds != 0 {
		if req.SamplingIntervalSeconds < 1 || req.SamplingIntervalSeconds > 3600 {
			writeError(w, http.StatusBadRequest, "sampling_interval_seconds must be 1..3600")
			return
		}
		if err := s.store.SetConfigValue(r.Context(), samplingIntervalKey,
			strconv.Itoa(req.SamplingIntervalSeconds)); err != nil {
			s.log.Error("config: set interval failed", "err", err)
			writeError(w, http.StatusInternalServerError, "could not update interval")
			return
		}
	}

	if req.AlertMaxEmailsPerDay != 0 {
		if req.AlertMaxEmailsPerDay < 1 || req.AlertMaxEmailsPerDay > 1000 {
			writeError(w, http.StatusBadRequest, "alert_max_emails_per_day must be 1..1000")
			return
		}
		if err := s.store.SetConfigValue(r.Context(), maxEmailsKey,
			strconv.Itoa(req.AlertMaxEmailsPerDay)); err != nil {
			s.log.Error("config: set email cap failed", "err", err)
			writeError(w, http.StatusInternalServerError, "could not update email cap")
			return
		}
	}

	for _, t := range req.Thresholds {
		if t.Metric == "" {
			writeError(w, http.StatusBadRequest, "threshold metric is required")
			return
		}
		if err := s.store.UpsertThreshold(r.Context(), t); err != nil {
			s.log.Error("config: upsert threshold failed", "err", err, "metric", t.Metric)
			writeError(w, http.StatusInternalServerError, "could not update threshold")
			return
		}
	}

	// Thresholds changed: refresh the alerter's cache immediately rather than
	// waiting for its periodic reload, so new bounds take effect at once.
	s.alerter.Reload(r.Context())

	payload, err := s.currentConfig(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "updated, but could not re-read config")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// currentConfig reads the sampling interval and thresholds into a payload.
func (s *Server) currentConfig(r *http.Request) (configPayload, error) {
	interval := 5 // sensible fallback if the row is somehow missing
	if v, err := s.store.GetConfigValue(r.Context(), samplingIntervalKey); err == nil {
		if n, perr := strconv.Atoi(v); perr == nil {
			interval = n
		}
	}
	maxEmails := 6
	if v, err := s.store.GetConfigValue(r.Context(), maxEmailsKey); err == nil {
		if n, perr := strconv.Atoi(v); perr == nil {
			maxEmails = n
		}
	}
	thresholds, err := s.store.GetThresholds(r.Context())
	if err != nil {
		return configPayload{}, err
	}
	if thresholds == nil {
		thresholds = []store.Threshold{}
	}
	return configPayload{
		SamplingIntervalSeconds: interval,
		AlertMaxEmailsPerDay:    maxEmails,
		Thresholds:              thresholds,
	}, nil
}
