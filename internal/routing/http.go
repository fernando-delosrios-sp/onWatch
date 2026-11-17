package routing

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed ui/*
var uiFS embed.FS

// Service bundles Store, Engine, Poller, and HTTP Handler for clean integration into onWatch.
type Service struct {
	Store   *Store
	Engine  *Engine
	Poller  *Poller
	Handler *Handler
}

// NewService initializes routing storage, engine, background poller, and HTTP handlers on db.
func NewService(db *sql.DB, logger *slog.Logger, pollInterval time.Duration) (*Service, error) {
	store, err := NewStore(db)
	if err != nil {
		return nil, err
	}
	engine := NewEngine(store, NewSQLiteProbeReader(db))
	poller := NewPoller(store, logger, pollInterval)
	handler := NewHandler(engine, store, poller)
	return &Service{
		Store:   store,
		Engine:  engine,
		Poller:  poller,
		Handler: handler,
	}, nil
}

// Start begins the background probe poller.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.Poller == nil {
		return
	}
	go s.Poller.Run(ctx)
}

// Handler serves the routing HTTP API and embedded /routing UI.
type Handler struct {
	engine  *Engine
	store   *Store
	poller  *Poller
	pageTpl *template.Template
}

// NewHandler constructs a routing HTTP Handler.
func NewHandler(engine *Engine, store *Store, poller *Poller) *Handler {
	tpl := template.Must(template.ParseFS(uiFS, "ui/routing.html"))
	return &Handler{
		engine:  engine,
		store:   store,
		poller:  poller,
		pageTpl: tpl,
	}
}

// RegisterRoutes mounts all routing UI and API routes onto mux under basePath.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, basePath string) {
	if h == nil || mux == nil {
		return
	}
	p := func(path string) string { return basePath + path }

	mux.HandleFunc(p("/routing"), func(w http.ResponseWriter, r *http.Request) {
		h.servePage(w, r, basePath)
	})
	mux.HandleFunc(p("/routing/"), func(w http.ResponseWriter, r *http.Request) {
		h.servePage(w, r, basePath)
	})
	mux.HandleFunc(p("/routing/assets/routing.css"), func(w http.ResponseWriter, r *http.Request) {
		h.serveAsset(w, "ui/routing.css", "text/css; charset=utf-8")
	})
	mux.HandleFunc(p("/routing/assets/routing.js"), func(w http.ResponseWriter, r *http.Request) {
		h.serveAsset(w, "ui/routing.js", "application/javascript; charset=utf-8")
	})

	mux.HandleFunc(p("/api/route"), h.Route)
	mux.HandleFunc(p("/api/route/feedback"), h.Feedback)
	mux.HandleFunc(p("/api/route/catalog"), h.Catalog)

	mux.HandleFunc(p("/api/routing/state"), h.State)
	mux.HandleFunc(p("/api/routing/pools/"), h.PoolsSubroute)
	mux.HandleFunc(p("/api/routing/models"), h.Models)
	mux.HandleFunc(p("/api/routing/models/"), h.ModelByID)
	mux.HandleFunc(p("/api/routing/excluded"), h.Excluded)
	mux.HandleFunc(p("/api/routing/settings"), h.Settings)
}

func (h *Handler) servePage(w http.ResponseWriter, r *http.Request, basePath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var buf bytes.Buffer
	if err := h.pageTpl.Execute(&buf, struct{ BasePath string }{BasePath: basePath}); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) serveAsset(w http.ResponseWriter, name, contentType string) {
	data, err := uiFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, &http.Request{})
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// Route handles GET /api/route?harnesses=...
func (h *Handler) Route(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	harnesses := r.URL.Query()["harnesses"]
	if raw := r.URL.Query().Get("harness"); raw != "" {
		harnesses = append(harnesses, raw)
	}
	resp, err := h.engine.SelectRoute(r.Context(), harnesses, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Feedback handles POST /api/route/feedback
func (h *Handler) Feedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in FeedbackInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ev, health, err := h.engine.ApplyFeedback(r.Context(), in, time.Now().UTC())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"event":  ev,
		"health": health,
	})
}

// Catalog handles GET/POST /api/route/catalog (YAML default, JSON optional).
func (h *Handler) Catalog(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if strings.EqualFold(r.URL.Query().Get("format"), "json") || strings.Contains(r.Header.Get("Accept"), "application/json") {
			cat, err := h.engine.BuildCatalog(time.Now().UTC())
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, cat)
			return
		}
		data, err := h.engine.ExportCatalogYAML(time.Now().UTC())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="onwatch-catalog.yaml"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodPost, http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read body")
			return
		}
		ct := strings.ToLower(r.Header.Get("Content-Type"))
		if strings.Contains(ct, "application/json") {
			// Accept either direct CatalogYAML JSON or {"yaml": "..."}.
			var wrapper struct {
				YAML string `json:"yaml"`
			}
			if json.Unmarshal(body, &wrapper) == nil && strings.TrimSpace(wrapper.YAML) != "" {
				if err := h.engine.ImportCatalogYAML([]byte(wrapper.YAML)); err != nil {
					writeJSONError(w, http.StatusBadRequest, err.Error())
					return
				}
			} else {
				var cat CatalogYAML
				if err := json.Unmarshal(body, &cat); err != nil {
					writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json catalog: %v", err))
					return
				}
				if err := h.engine.ImportCatalog(&cat); err != nil {
					writeJSONError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
		} else {
			if err := h.engine.ImportCatalogYAML(body); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		cat, err := h.engine.BuildCatalog(time.Now().UTC())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"catalog": cat,
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// State handles GET /api/routing/state for the embedded UI.
func (h *Handler) State(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	now := time.Now().UTC()
	harnesses := r.URL.Query()["harnesses"]
	route, err := h.engine.SelectRoute(r.Context(), harnesses, now)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	models, err := h.store.ListModels()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	excluded, err := h.store.ListExcludedModels()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	feedback, err := h.store.ListRecentFeedback(50)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var probeSettings ProbeSettings
	if h.poller != nil {
		probeSettings = h.poller.GetProbeSettings()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generated_at":    now,
		"route":           route,
		"pools":           route.Pools,
		"models":          models,
		"excluded_models": excluded,
		"recent_feedback": feedback,
		"probe_settings":  probeSettings,
	})
}

// PoolsSubroute dispatches /api/routing/pools/{id}, /api/routing/pools/{id}/clear-cooldown, /api/routing/pools/{id}/probe.
func (h *Handler) PoolsSubroute(w http.ResponseWriter, r *http.Request) {
	idx := strings.Index(r.URL.Path, "/api/routing/pools/")
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	rest := strings.Trim(r.URL.Path[idx+len("/api/routing/pools/"):], "/")
	if rest == "" {
		writeJSONError(w, http.StatusBadRequest, "pool id required")
		return
	}
	parts := strings.Split(rest, "/")
	poolID := parts[0]

	if len(parts) == 2 && parts[1] == "clear-cooldown" {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		health, err := h.engine.ClearCooldown(poolID, time.Now().UTC())
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "health": health})
		return
	}

	if len(parts) == 2 && parts[1] == "probe" {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if h.poller == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "poller unavailable")
			return
		}
		snap, err := h.poller.ProbePool(r.Context(), poolID)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "snapshot": snap})
		return
	}

	if len(parts) != 1 || r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var p Pool
	if err := decodeJSONBody(r, &p); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.ID = poolID
	if existing, err := h.store.GetPool(poolID); err == nil && existing != nil {
		if p.DisplayName == "" {
			p.DisplayName = existing.DisplayName
		}
		if p.ProbeSource == "" {
			p.ProbeSource = existing.ProbeSource
		}
		if p.CycleStart == nil {
			p.CycleStart = existing.CycleStart
		}
	}
	p.UpdatedAt = time.Now().UTC()
	if err := h.store.UpsertPool(p); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "pool": p})
}

// Models handles POST /api/routing/models (create/upsert) and GET /api/routing/models.
func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		models, err := h.store.ListModels()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"models": models})
	case http.MethodPost:
		var m RouteModel
		if err := decodeJSONBody(r, &m); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := h.store.UpsertModel(m)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		m.ID = id
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "model": m})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ModelByID handles PUT/DELETE /api/routing/models/{id}.
func (h *Handler) ModelByID(w http.ResponseWriter, r *http.Request) {
	idx := strings.Index(r.URL.Path, "/api/routing/models/")
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	idStr := strings.Trim(r.URL.Path[idx+len("/api/routing/models/"):], "/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid model id")
		return
	}

	switch r.Method {
	case http.MethodPut:
		var m RouteModel
		if err := decodeJSONBody(r, &m); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		m.ID = id
		if _, err := h.store.UpsertModel(m); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "model": m})
	case http.MethodDelete:
		if err := h.store.DeleteModel(id); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Excluded handles GET/PUT /api/routing/excluded.
func (h *Handler) Excluded(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := h.store.ListExcludedModels()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"excluded_models": list})
	case http.MethodPut:
		var payload struct {
			ExcludedModels []string `json:"excluded_models"`
		}
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.store.SetExcludedModels(payload.ExcludedModels); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		list, _ := h.store.ListExcludedModels()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "excluded_models": list})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Settings handles GET/PUT /api/routing/settings.
func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var ps ProbeSettings
		if h.poller != nil {
			ps = h.poller.GetProbeSettings()
		}
		writeJSON(w, http.StatusOK, ps)
	case http.MethodPut:
		if h.poller == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "poller unavailable")
			return
		}
		var payload struct {
			ChutesAPIKey      *string `json:"chutes_api_key"`
			OpenRouterPAYGKey *string `json:"openrouter_payg_api_key"`
			GoogleCloudProj   *string `json:"google_cloud_project"`
		}
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.poller.UpdateProbeSettings(payload.ChutesAPIKey, payload.OpenRouterPAYGKey, payload.GoogleCloudProj); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":             true,
			"probe_settings": h.poller.GetProbeSettings(),
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func decodeJSONBody(r *http.Request, dst interface{}) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("empty request body")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
