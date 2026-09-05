// Package httpapi exposes the source-management subset of the local HTTP API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /api/sources", a.listSources)
	mux.HandleFunc("GET /api/sources/", a.exportSource)
	mux.HandleFunc("POST /api/sources/import", a.importSource)
	mux.HandleFunc("PATCH /api/sources/", a.updateSource)
	mux.HandleFunc("POST /api/sources/", a.runSource)
	mux.HandleFunc("GET /api/vacancies", a.listVacancies)
	mux.HandleFunc("PATCH /api/vacancies/", a.updateVacancy)
	mux.HandleFunc("GET /api/runs/latest", a.latestRuns)
	return mux
}
func (a *API) listSources(w http.ResponseWriter, r *http.Request) {
	values, err := a.store.ListSources(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	reply(w, 200, values)
}
func (a *API) exportSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(sourceID(r.URL.Path), "/export")
	source, err := a.store.GetSource(r.Context(), id)
	if err != nil {
		fail(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
	_, _ = w.Write([]byte(source.ConfigYAML))
}
func (a *API) importSource(w http.ResponseWriter, r *http.Request) {
	var request struct {
		YAML string `json:"yaml"`
	}
	if err := decode(r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	if _, err := config.Parse([]byte(request.YAML)); err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, map[string]any{"yaml": request.YAML, "requires_dry_run": true})
}
func parseInt(value string) int { number, _ := strconv.Atoi(value); return number }
func (a *API) listVacancies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, total, err := a.store.ListVacancies(r.Context(), storage.VacancyFilter{SourceID: q.Get("source_id"), ListingStatus: q.Get("listing_status"), UserStatus: q.Get("user_status"), RunID: q.Get("run_id"), Search: q.Get("search"), Sort: q.Get("sort"), Direction: q.Get("direction"), Limit: parseInt(q.Get("limit")), Offset: parseInt(q.Get("offset"))})
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, map[string]any{"items": items, "total": total, "limit": q.Get("limit"), "offset": q.Get("offset")})
}
func (a *API) updateVacancy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/vacancies/")
	var request struct {
		UserStatus string `json:"user_status"`
	}
	if err := decode(r, &request); err != nil {
		fail(w, 400, err)
		return
	}
	updated, err := a.store.UpdateVacancyUserStatus(r.Context(), id, request.UserStatus)
	if errors.Is(err, storage.ErrNotFound) {
		fail(w, 404, err)
		return
	}
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, updated)
}
func (a *API) latestRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := a.store.LatestRuns(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	reply(w, 200, runs)
}

type sourceRequest struct {
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	YAML             string `json:"yaml"`
	TestToken        string `json:"test_token"`
	IsActive         *bool  `json:"is_active"`
	ScheduleType     string `json:"schedule_type"`
	ScheduleValue    string `json:"schedule_value"`
	ScheduleTimezone string `json:"schedule_timezone"`
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
	created, err := a.store.CreateSource(r.Context(), storage.NewSource{ID: uuid.NewString(), Slug: request.Slug, Name: name, ConfigYAML: request.YAML, IsActive: active, ScheduleType: request.ScheduleType, ScheduleValue: request.ScheduleValue, ScheduleTimezone: request.ScheduleTimezone})
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
	if request.ScheduleType != "" {
		current.ScheduleType = request.ScheduleType
		current.ScheduleValue = request.ScheduleValue
		current.ScheduleTimezone = request.ScheduleTimezone
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
	cfg, err := config.Parse([]byte(source.ConfigYAML))
	if err != nil {
		fail(w, 422, err)
		return
	}
	run, err := a.store.StartExclusiveRun(r.Context(), storage.NewRun{ID: uuid.NewString(), SourceID: id, StartedAt: time.Now().UTC()})
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
