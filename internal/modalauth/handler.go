package modalauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type Handler struct {
	store *Store
	jobs  *JobManager
}

func NewHandler(store *Store) *Handler {
	return &Handler{
		store: store,
		jobs:  NewJobManager(store),
	}
}

func (h *Handler) SetOnSetupDone(fn func(accountID, email, baseURL, apiKey, stripeURL string)) {
	h.jobs.SetOnSetupDone(fn)
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accounts", h.listAccounts)
	mux.HandleFunc("DELETE /accounts/{id}", h.deleteAccount)
	mux.HandleFunc("GET /accounts/{id}/cookie", h.getAccountCookie)
	mux.HandleFunc("POST /batch", h.batchImport)
	mux.HandleFunc("GET /jobs", h.listJobs)
	mux.HandleFunc("DELETE /jobs", h.clearJobs)
	mux.HandleFunc("POST /jobs/action", h.jobAction)
	mux.HandleFunc("POST /accounts/{id}/setup", h.runSetup)
	mux.HandleFunc("POST /accounts/{id}/sync-balance", h.syncBalance)
	mux.HandleFunc("POST /accounts/{id}/verify-payment", h.verifyPayment)
	mux.HandleFunc("GET /setup-jobs", h.listSetupJobs)
	mux.HandleFunc("DELETE /setup-jobs", h.clearSetupJobs)
	mux.HandleFunc("GET /settings", h.getSettings)
	mux.HandleFunc("POST /settings", h.updateSettings)
	mux.HandleFunc("GET /state", h.getState)
	return mux
}

func (h *Handler) getState(w http.ResponseWriter, r *http.Request) {
	accounts := h.store.ListAccounts()
	jobs, running, active := h.jobs.ListJobs()
	setupJobs := h.jobs.ListSetupJobs()
	settings := h.store.GetSettings()
	writeJSON(w, map[string]interface{}{
		"accounts":   accounts,
		"jobs":       jobs,
		"setupJobs":  setupJobs,
		"running":    running,
		"active":     active,
		"settings":   settings,
	})
}

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.store.ListAccounts())
}

func (h *Handler) getAccountCookie(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, cookie, _, err := h.store.GetAccountCookie(id)
	if err != nil {
		if errors.Is(err, errNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"cookie": cookie})
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteAccount(id); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

type batchInput struct {
	Text     string `json:"text"`
	Name     string `json:"name"`
	ProxyURL string `json:"proxyUrl"`
	Mode     string `json:"mode"`
	Headless bool   `json:"headless"`
}

func (h *Handler) batchImport(w http.ResponseWriter, r *http.Request) {
	var in batchInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	queued, errs, jobs := h.jobs.AddBatch(in.Text, in.Name, in.ProxyURL, in.Mode, in.Headless)
	if queued == 0 {
		http.Error(w, "no valid accounts; format: email|password[|auxEmail]", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{
		"queued": queued,
		"errors": errs,
		"jobs":   jobs,
	})
}

func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, running, active := h.jobs.ListJobs()
	writeJSON(w, map[string]interface{}{
		"jobs":    jobs,
		"running": running,
		"active":  active,
	})
}

func (h *Handler) clearJobs(w http.ResponseWriter, r *http.Request) {
	jobs := h.jobs.ClearFinished()
	writeJSON(w, map[string]interface{}{"jobs": jobs})
}

func (h *Handler) jobAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	op := strings.TrimSpace(r.URL.Query().Get("op"))
	if id == "" || (op != "cancel" && op != "retry") {
		http.Error(w, "id and op (cancel|retry) required", http.StatusBadRequest)
		return
	}
	jobs, err := h.jobs.JobAction(id, op)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"jobs": jobs})
}

func (h *Handler) runSetup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, cookie, proxyURL, err := h.store.GetAccountCookie(id)
	if err != nil {
		http.Error(w, "account not found or cookie missing", http.StatusNotFound)
		return
	}
	var in struct {
		Headless bool `json:"headless"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	job, err := h.jobs.RunSetup(id, acc.Email, cookie, proxyURL, in.Headless)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, job)
}

func (h *Handler) listSetupJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"jobs": h.jobs.ListSetupJobs()})
}

func (h *Handler) clearSetupJobs(w http.ResponseWriter, r *http.Request) {
	h.store.ClearSetupJobs()
	writeJSON(w, map[string]string{"status": "cleared"})
}

func (h *Handler) syncBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.SyncBalance(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, h.store.ListAccounts())
}

func (h *Handler) verifyPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, cookie, proxyURL, err := h.store.GetAccountCookie(id)
	if err != nil {
		http.Error(w, "account not found or cookie missing", http.StatusNotFound)
		return
	}
	settings := h.store.GetSettings()
	baseURL := settings.BaseURL
	if baseURL == "" {
		baseURL = "https://modal.com"
	}
	workspace := acc.Workspace
	if workspace == "" {
		workspace = workspaceFromURL(acc.WorkspaceURL)
	}
	if workspace == "" {
		http.Error(w, "workspace unknown; run setup first", http.StatusBadRequest)
		return
	}
	verifyURL := baseURL + "/api/stripe/" + workspace + "/await-payment-verification?next=%2Fhome"
	result, err := fetchVerifyPayment(verifyURL, cookie, proxyURL)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, result)
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.store.GetSettings())
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	out, err := h.store.UpdateSettings(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

var errNotFound = errors.New("not found")

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
