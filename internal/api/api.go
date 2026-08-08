package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"modals-router/internal/balancer"
	"modals-router/internal/models"
	"modals-router/internal/proxy"
	"modals-router/internal/store"
)

type API struct {
	store    *store.Store
	balancer *balancer.Balancer
	proxy    *proxy.Proxy
	token    string
}

func New(s *store.Store, b *balancer.Balancer, p *proxy.Proxy, token string) *API {
	return &API{store: s, balancer: b, proxy: p, token: token}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels", a.auth(a.listChannels))
	mux.HandleFunc("POST /channels", a.auth(a.createChannel))
	mux.HandleFunc("GET /channels/{id}", a.auth(a.getChannel))
	mux.HandleFunc("PUT /channels/{id}", a.auth(a.updateChannel))
	mux.HandleFunc("DELETE /channels/{id}", a.auth(a.deleteChannel))
	mux.HandleFunc("POST /channels/{id}/enable", a.auth(a.enableChannel))
	mux.HandleFunc("POST /channels/{id}/disable", a.auth(a.disableChannel))
	mux.HandleFunc("POST /channels/{id}/reset", a.auth(a.resetStats))
	mux.HandleFunc("POST /channels/{id}/test", a.auth(a.testChannel))
	mux.HandleFunc("GET /stats", a.auth(a.stats))
	mux.HandleFunc("GET /health", a.health)
	return mux
}

func (a *API) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token != "" {
			token := r.Header.Get("X-Admin-Token")
			if token == "" {
				auth := r.Header.Get("Authorization")
				if strings.HasPrefix(auth, "Bearer ") {
					token = strings.TrimPrefix(auth, "Bearer ")
				}
			}
			if token != a.token {
				jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) {
	channels := a.store.ListChannels()
	for i := range channels {
		channels[i].Key = maskKey(channels[i].Key)
	}
	jsonOK(w, channels)
}

func (a *API) getChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch, ok := a.store.GetChannel(id)
	if !ok {
		jsonError(w, "channel not found", http.StatusNotFound)
		return
	}
	ch.Key = maskKey(ch.Key)
	jsonOK(w, ch)
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	var ch models.Channel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if ch.Name == "" || ch.URL == "" || ch.Key == "" {
		jsonError(w, "name, url, and key are required", http.StatusBadRequest)
		return
	}
	created, err := a.store.CreateChannel(ch)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.balancer.Reload(a.store.ListChannels())
	created.Key = maskKey(created.Key)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

func (a *API) updateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ch models.Channel
	if err := json.NewDecoder(r.Body).Decode(&ch); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if ch.Name == "" || ch.URL == "" {
		jsonError(w, "name and url are required", http.StatusBadRequest)
		return
	}
	existing, ok := a.store.GetChannel(id)
	if !ok {
		jsonError(w, "channel not found", http.StatusNotFound)
		return
	}
	if ch.Key == "" || strings.HasPrefix(ch.Key, "***") {
		ch.Key = existing.Key
	}
	if err := a.store.UpdateChannel(id, ch); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.balancer.Reload(a.store.ListChannels())
	updated, _ := a.store.GetChannel(id)
	updated.Key = maskKey(updated.Key)
	jsonOK(w, updated)
}

func (a *API) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.DeleteChannel(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	a.balancer.Reload(a.store.ListChannels())
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) enableChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.EnableChannel(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	a.balancer.Reload(a.store.ListChannels())
	jsonOK(w, map[string]string{"status": "enabled"})
}

func (a *API) disableChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "manually disabled"
	}
	if err := a.store.DisableChannel(id, reason); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	a.balancer.Reload(a.store.ListChannels())
	jsonOK(w, map[string]string{"status": "disabled"})
}

func (a *API) resetStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.store.ResetStats(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]string{"status": "reset"})
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	stats := a.store.GetStats()
	jsonOK(w, stats)
}

func (a *API) testChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ch, ok := a.store.GetChannel(id)
	if !ok {
		jsonError(w, "channel not found", http.StatusNotFound)
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	result := a.proxy.TestChannel(ch, req.Model)
	log.Printf("test: channel=%s model=%s status=%d latency=%dms",
		ch.Name, req.Model, result.StatusCode, result.LatencyMS)
	jsonOK(w, result)
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]interface{}{
		"status":          "ok",
		"active_channels": a.balancer.Count(),
	})
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
