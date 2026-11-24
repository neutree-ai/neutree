package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

const (
	headerAdminToken = "X-Neutree-Admin-Token" //nolint:gosec // HTTP header name, not a credential
	headerUsageToken = "X-Neutree-Usage-Token" //nolint:gosec // HTTP header name, not a credential
	maxAdminBody     = 1 << 20
)

func (a *gatewayApp) buildDataMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/internal/v1/usage", a.handleUsage)
	mux.HandleFunc("/admin/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	})
	mux.Handle("/", a.proxy)

	return mux
}

func (a *gatewayApp) buildAdminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/admin/v1/events", a.withAdminAuth(a.handleEvent))
	mux.HandleFunc("/admin/v1/snapshot", a.withAdminAuth(a.handleSnapshot))
	mux.HandleFunc("/admin/v1/usage", a.withAdminAuth(a.handleUsageView))
	mux.HandleFunc("/internal/v1/usage", a.handleUsage)

	return mux
}

func (a *gatewayApp) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.adminToken != "" && !matchesAdminToken(r, a.adminToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		next(w, r)
	}
}

func matchesAdminToken(r *http.Request, expected string) bool {
	candidate := strings.TrimSpace(r.Header.Get(headerAdminToken))
	if candidate == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
			candidate = strings.TrimSpace(auth[7:])
		}
	}

	return subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1
}

func (a *gatewayApp) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	defer r.Body.Close()

	var event state.Event
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody)).Decode(&event); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_event"})
		return
	}

	if !validEvent(event) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_event"})
		return
	}

	if err := a.engine.PublishAndWait(r.Context(), event); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "applied",
		"version": a.engine.Snapshot().Version,
	})
}

func validEvent(event state.Event) bool {
	switch event.Kind {
	case state.EventConfig:
		return event.Config != nil
	case state.EventQuota:
		return event.Quota != nil
	case state.EventRuntime:
		return event.Runtime != nil
	default:
		return false
	}
}

func (a *gatewayApp) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"config":  a.engine.Snapshot(),
		"runtime": a.engine.RuntimeTable().Snapshot(),
	})
}

func (a *gatewayApp) handleUsageView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pools":         a.leases.Pools(),
		"active_leases": a.leases.ActiveLeases(),
		"bills":         a.leases.Bills(),
	})
}

func (a *gatewayApp) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	if len(a.callbackSecret) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "callback_disabled"})
		return
	}

	token := strings.TrimSpace(r.Header.Get(headerUsageToken))
	if token == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}

	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing_usage_token"})
		return
	}

	defer r.Body.Close()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdminBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body_too_large"})
		return
	}

	record, err := quota.VerifyUsageCallback(bytes.TrimSpace(body), token, a.callbackSecret)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_usage_token"})
		return
	}

	delta, err := a.leases.Correct(record)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "applied", "delta": delta})
}
