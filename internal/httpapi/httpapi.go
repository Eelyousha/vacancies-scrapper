// Package httpapi exposes the source-management subset of the local HTTP API.
package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
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

//go:embed templates/dashboard.html templates/source_builder.html static/app.css
var dashboardAssets embed.FS

var dashboardTemplate = template.Must(template.ParseFS(dashboardAssets, "templates/dashboard.html"))
var sourceBuilderTemplate = template.Must(template.ParseFS(dashboardAssets, "templates/source_builder.html"))

type API struct {
	store      *storage.Store
	dry        *dryrun.Service
	scraper    dryrun.Scraper
	runTimeout time.Duration
}

// DefaultManualRunTimeout bounds browser work started through the local HTTP
// interface. It deliberately exceeds ordinary HTTP request deadlines: a manual
// run is a server operation, not work owned by the client connection.
const DefaultManualRunTimeout = 30 * time.Minute

func New(store *storage.Store, dry *dryrun.Service, browser dryrun.Scraper) http.Handler {
	return NewWithRunTimeout(store, dry, browser, DefaultManualRunTimeout)
}

// NewWithRunTimeout creates the HTTP handler with a server-side timeout for
// synchronous manual source runs. A non-positive value falls back to the
// default so programmatic callers cannot accidentally create unbounded runs.
func NewWithRunTimeout(store *storage.Store, dry *dryrun.Service, browser dryrun.Scraper, runTimeout time.Duration) http.Handler {
	if runTimeout <= 0 {
		runTimeout = DefaultManualRunTimeout
	}
	a := &API{store: store, dry: dry, scraper: browser, runTimeout: runTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.dashboard)
	mux.HandleFunc("GET /static/app.css", a.dashboardCSS)
	mux.HandleFunc("GET /sources", a.sourceBuilder)
	mux.HandleFunc("POST /sources/test", a.testSourceBuilder)
	mux.HandleFunc("POST /sources", a.createSourceBuilder)
	mux.HandleFunc("POST /sources/{id}/run", a.runSourceBuilder)
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

type dashboardVacancy struct {
	storage.Vacancy
	SourceName string
}

type dashboardRun struct {
	storage.Run
	SourceName    string
	StartedAtText string
}

type dashboardData struct {
	Vacancies []dashboardVacancy
	Sources   []storage.Source
	Runs      []dashboardRun
	Total     int
	Filter    storage.VacancyFilter
}

// sourceBuilderData holds exactly the submitted YAML because dry-run tokens are
// intentionally bound to its bytes, rather than to a parsed representation.
type sourceBuilderData struct {
	Slug      string
	Name      string
	YAML      string
	TestToken string
	ExpiresAt time.Time
	Preview   []scraper.Vacancy
	Error     string
	Sources   []sourceBuilderSource
}

// sourceBuilderSource adds presentation-only data to the persistent source.
// In particular, a zero LastRunAt should be shown as an understandable state,
// rather than as Go's zero timestamp.
type sourceBuilderSource struct {
	storage.Source
	LastRunAtText string
}

// dashboard reads directly from storage so the local UI does not depend on its
// own HTTP API and remains usable when the server is bound to localhost only.
func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := normalizePagination(parseInt(q.Get("limit")), parseInt(q.Get("offset")))
	filter := storage.VacancyFilter{
		SourceID: q.Get("source_id"), ListingStatus: q.Get("listing_status"),
		UserStatus: q.Get("user_status"), Search: q.Get("search"),
		Limit: limit, Offset: offset,
	}
	items, total, err := a.store.ListVacancies(r.Context(), filter)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err)
		return
	}
	sources, err := a.store.ListSources(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	runs, err := a.store.LatestRuns(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	sourceNames := make(map[string]string, len(sources))
	for _, source := range sources {
		sourceNames[source.ID] = source.Name
	}
	data := dashboardData{Sources: sources, Total: total, Filter: filter}
	for _, item := range items {
		data.Vacancies = append(data.Vacancies, dashboardVacancy{Vacancy: item, SourceName: sourceNames[item.SourceID]})
	}
	for _, run := range runs {
		data.Runs = append(data.Runs, dashboardRun{
			Run: run, SourceName: sourceNames[run.SourceID],
			StartedAtText: run.StartedAt.Local().Format("02.01.2006 15:04"),
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		// Headers may already be written, but logging an execution failure is less
		// useful than returning the same API-shaped error before that point.
		return
	}
}

func (a *API) dashboardCSS(w http.ResponseWriter, _ *http.Request) {
	stylesheet, err := dashboardAssets.ReadFile("static/app.css")
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(stylesheet)
}

func (a *API) sourceBuilder(w http.ResponseWriter, r *http.Request) {
	a.renderSourceBuilder(w, r, http.StatusOK, sourceBuilderData{})
}

func (a *API) testSourceBuilder(w http.ResponseWriter, r *http.Request) {
	form, err := sourceBuilderForm(r)
	if err != nil {
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, sourceBuilderData{Error: err.Error()})
		return
	}
	result, err := a.dry.Test(r.Context(), []byte(form.YAML))
	if err != nil {
		form.Error = err.Error()
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	form.TestToken = result.Token
	form.ExpiresAt = result.ExpiresAt
	form.Preview = result.Preview
	a.renderSourceBuilder(w, r, http.StatusOK, form)
}

func (a *API) createSourceBuilder(w http.ResponseWriter, r *http.Request) {
	form, err := sourceBuilderForm(r)
	if err != nil {
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, sourceBuilderData{Error: err.Error()})
		return
	}
	source, err := config.Parse([]byte(form.YAML))
	if err != nil {
		form.Error = err.Error()
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	if err = a.dry.ConsumeToken([]byte(form.YAML), source.BaseURL, form.TestToken); err != nil {
		form.Error = err.Error()
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	name := form.Name
	if name == "" {
		name = source.SiteName
	}
	if _, err = a.store.CreateSource(r.Context(), storage.NewSource{
		ID: uuid.NewString(), Slug: form.Slug, Name: name, ConfigYAML: form.YAML, IsActive: true,
	}); err != nil {
		form.Error = err.Error()
		a.renderSourceBuilder(w, r, http.StatusUnprocessableEntity, form)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *API) runSourceBuilder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		a.renderSourceBuilder(w, r, http.StatusBadRequest, sourceBuilderData{Error: err.Error()})
		return
	}
	if _, status, err := a.executeSourceRunWithQuery(r.Context(), id, r.PostForm.Get("search_query")); err != nil {
		a.renderSourceBuilder(w, r, status, sourceBuilderData{Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/?source_id="+id, http.StatusSeeOther)
}

func sourceBuilderForm(r *http.Request) (sourceBuilderData, error) {
	if err := r.ParseForm(); err != nil {
		return sourceBuilderData{}, err
	}
	return sourceBuilderData{
		Slug: r.PostForm.Get("slug"), Name: r.PostForm.Get("name"), YAML: r.PostForm.Get("yaml"),
		TestToken: r.PostForm.Get("test_token"),
	}, nil
}

func (a *API) renderSourceBuilder(w http.ResponseWriter, r *http.Request, status int, data sourceBuilderData) {
	sources, err := a.store.ListSources(r.Context())
	if err != nil {
		status = http.StatusInternalServerError
		data.Error = err.Error()
	} else {
		data.Sources = make([]sourceBuilderSource, 0, len(sources))
		for _, source := range sources {
			lastRunAtText := "ещё не запускался"
			if !source.LastRunAt.IsZero() {
				lastRunAtText = source.LastRunAt.Local().Format("02.01.2006 15:04")
			}
			data.Sources = append(data.Sources, sourceBuilderSource{Source: source, LastRunAtText: lastRunAtText})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = sourceBuilderTemplate.Execute(w, data)
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

func normalizePagination(limit, offset int) (int, int) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (a *API) listVacancies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := normalizePagination(parseInt(q.Get("limit")), parseInt(q.Get("offset")))
	items, total, err := a.store.ListVacancies(r.Context(), storage.VacancyFilter{SourceID: q.Get("source_id"), ListingStatus: q.Get("listing_status"), UserStatus: q.Get("user_status"), RunID: q.Get("run_id"), Search: q.Get("search"), Sort: q.Get("sort"), Direction: q.Get("direction"), Limit: limit, Offset: offset})
	if err != nil {
		fail(w, 422, err)
		return
	}
	reply(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
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
	completed, status, err := a.executeSourceRun(r.Context(), id)
	if err != nil {
		fail(w, status, err)
		return
	}
	reply(w, http.StatusOK, completed)
}

// executeSourceRun is the single synchronous lifecycle for API and HTML manual
// runs. Keeping the two entry points here prevents either one from bypassing
// exclusive-start locking or leaving a source marked running after a failure.
func (a *API) executeSourceRun(ctx context.Context, id string) (storage.Run, int, error) {
	return a.executeSourceRunWithQuery(ctx, id, "")
}

// executeSourceRunWithQuery keeps the JSON API contract unchanged while
// allowing the HTML manual-run form to provide a one-off search phrase.
func (a *API) executeSourceRunWithQuery(ctx context.Context, id, query string) (storage.Run, int, error) {
	// Do not inherit the request context here. Browsers and proxies commonly
	// cancel a form request before a full scrape has finished; the started run
	// must still reach a terminal state in the persistent run log.
	_ = ctx // Kept in the signature for API compatibility with existing callers.
	persistenceCtx := context.Background()
	source, err := a.store.GetSource(persistenceCtx, id)
	if err != nil {
		return storage.Run{}, http.StatusNotFound, err
	}
	cfg, err := config.Parse([]byte(source.ConfigYAML))
	if err != nil {
		return storage.Run{}, http.StatusUnprocessableEntity, err
	}
	run, err := a.store.StartExclusiveRun(persistenceCtx, storage.NewRun{ID: uuid.NewString(), SourceID: id, StartedAt: time.Now().UTC()})
	if errors.Is(err, storage.ErrInvalidTransition) {
		log.Printf("manual run rejected source_id=%q source_slug=%q error=%q", id, source.Slug, err)
		return storage.Run{}, http.StatusConflict, err
	}
	if err != nil {
		log.Printf("manual run could not start source_id=%q source_slug=%q error=%q", id, source.Slug, err)
		return storage.Run{}, http.StatusInternalServerError, err
	}
	log.Printf("manual run started source_id=%q source_slug=%q run_id=%q query=%q", source.ID, source.Slug, run.ID, query)
	runCtx, cancel := context.WithTimeout(context.Background(), a.runTimeout)
	defer cancel()
	result, err := scrapeSourceWithQuery(runCtx, a.scraper, cfg, query)
	logger := runlog.New(a.store)
	if err != nil {
		err = recordManualRunFailure(logger, source, run, err)
		if errors.Is(err, context.DeadlineExceeded) {
			return storage.Run{}, http.StatusGatewayTimeout, err
		}
		return storage.Run{}, http.StatusBadGateway, err
	}
	result.FinishedAt = time.Now().UTC()
	var fields []string
	if cfg.Identity != nil {
		fields = cfg.Identity.FallbackFields
	}
	completed, err := logger.Finish(persistenceCtx, run.ID, result, fields, storage.CompletionStatusComplete)
	if err != nil {
		err = recordManualRunFailure(logger, source, run, err)
		return storage.Run{}, http.StatusInternalServerError, err
	}
	log.Printf("manual run finished source_id=%q source_slug=%q run_id=%q status=%q added=%d updated=%d archived=%d seen=%d", source.ID, source.Slug, completed.ID, completed.Status, completed.AddedCount, completed.UpdatedCount, completed.ArchivedCount, completed.SeenCount)
	return completed, http.StatusOK, nil
}

// recordManualRunFailure always attempts to release the source's exclusive
// running state. If that finalisation itself fails, return both diagnostics so
// callers do not mistake a still-running run for a completed failure.
func recordManualRunFailure(logger runlog.Logger, source storage.Source, run storage.Run, cause error) error {
	if _, err := logger.Fail(context.Background(), run.ID, cause.Error()); err != nil {
		log.Printf("manual run failure persistence failed source_id=%q source_slug=%q run_id=%q cause=%q persistence_error=%q", source.ID, source.Slug, run.ID, cause, err)
		return errors.Join(cause, fmt.Errorf("record failed manual run %q: %w", run.ID, err))
	}
	log.Printf("manual run failed source_id=%q source_slug=%q run_id=%q error=%q", source.ID, source.Slug, run.ID, cause)
	return cause
}

type queryScraper interface {
	ScrapeWithQuery(context.Context, config.Source, string) (scraper.Result, error)
}

func scrapeSourceWithQuery(ctx context.Context, browser dryrun.Scraper, source config.Source, query string) (scraper.Result, error) {
	if strings.TrimSpace(query) == "" {
		return browser.Scrape(ctx, source)
	}
	if searchable, ok := browser.(queryScraper); ok {
		return searchable.ScrapeWithQuery(ctx, source, query)
	}
	return scraper.Result{}, errors.New("manual search is not supported by this scraper")
}

var _ dryrun.Scraper = scraper.Scraper{}
