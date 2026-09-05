// Package httpapi exposes the source-management subset of the local HTTP API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/runlog"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

type API struct {
	store   *storage.Store
	dry     *dryrun.Service
	scraper dryrun.Scraper
}

func New(store *storage.Store, dry *dryrun.Service, browser dryrun.Scraper) http.Handler {
	a := &API{store: store, dry: dry, scraper: browser}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sources/test-config", a.testConfig)
	mux.HandleFunc("POST /api/sources", a.createSource)
	mux.HandleFunc("PATCH /api/sources/", a.updateSource)
	mux.HandleFunc("POST /api/sources/", a.runSource)
	return mux
}

type sourceRequest struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	YAML      string `json:"yaml"`
	TestToken string `json:"test_token"`
	IsActive  *bool  `json:"is_active"`
}

func decode(r *http.Request, into any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(into)
}
func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

func (a *API) testConfig(w http.ResponseWriter, r *http.Request) {
	var request struct {
		YAML string `json:"yaml"`
	}
	if err := decode(r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	result, err := a.dry.Test(r.Context(), []byte(request.YAML))
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, result)
}
func (a *API) createSource(w http.ResponseWriter, r *http.Request) {
	var request sourceRequest
	if err := decode(r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	source, err := config.Parse([]byte(request.YAML))
	if err != nil {
		fail(w, 422, err)
		return
	}
	if err = a.dry.ConsumeToken([]byte(request.YAML), source.BaseURL, request.TestToken); err != nil {
		fail(w, 403, err)
		return
	}
	name := request.Name
	if name == "" {
		name = source.SiteName
	}
	active := true
	if request.IsActive != nil {
		active = *request.IsActive
	}
	created, err := a.store.CreateSource(r.Context(), storage.NewSource{ID: uuid.NewString(), Slug: request.Slug, Name: name, ConfigYAML: request.YAML, IsActive: active})
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 201, created)
}
func sourceID(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, "/api/sources/"), "/run")
}
func (a *API) updateSource(w http.ResponseWriter, r *http.Request) {
	id := sourceID(r.URL.Path)
	var request sourceRequest
	if err := decode(r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	current, err := a.store.GetSource(r.Context(), id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if request.YAML != "" {
		parsed, parseErr := config.Parse([]byte(request.YAML))
		if parseErr != nil {
			fail(w, 422, parseErr)
			return
		}
		if err = a.dry.ConsumeToken([]byte(request.YAML), parsed.BaseURL, request.TestToken); err != nil {
			fail(w, 403, err)
			return
		}
		current.ConfigYAML = request.YAML
		if request.Name == "" {
			current.Name = parsed.SiteName
		}
	}
	if request.Name != "" {
		current.Name = request.Name
	}
	if request.Slug != "" {
		current.Slug = request.Slug
	}
	if request.IsActive != nil {
		current.IsActive = *request.IsActive
	}
	updated, err := a.store.UpdateSource(r.Context(), current)
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, updated)
}
func (a *API) runSource(w http.ResponseWriter, r *http.Request) {
	id := sourceID(r.URL.Path)
	source, err := a.store.GetSource(r.Context(), id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	if source.Status == storage.SourceStatusRunning {
		fail(w, http.StatusConflict, storage.ErrInvalidTransition)
		return
	}
	cfg, err := config.Parse([]byte(source.ConfigYAML))
	if err != nil {
		fail(w, 422, err)
		return
	}
	run, err := a.store.StartRun(r.Context(), storage.NewRun{ID: uuid.NewString(), SourceID: id, StartedAt: time.Now().UTC()})
	if errors.Is(err, storage.ErrInvalidTransition) {
		fail(w, 409, err)
		return
	}
	if err != nil {
		fail(w, 500, err)
		return
	}
	result, err := a.scraper.Scrape(r.Context(), cfg)
	logger := runlog.New(a.store)
	if err != nil {
		_, _ = logger.Fail(context.Background(), run.ID, err.Error())
		fail(w, 502, err)
		return
	}
	result.FinishedAt = time.Now().UTC()
	var fields []string
	if cfg.Identity != nil {
		fields = cfg.Identity.FallbackFields
	}
	completed, err := logger.Finish(r.Context(), run.ID, result, fields, storage.CompletionStatusComplete)
	if err != nil {
		fail(w, 500, err)
		return
	}
	reply(w, 200, completed)
}

var _ dryrun.Scraper = scraper.Scraper{}
