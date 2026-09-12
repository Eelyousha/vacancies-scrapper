package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func TestSourcesPageListsSavedSourceAndManualRunForm(t *testing.T) {
	handler, store := sourceBuilderHandler(t)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sources", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /sources = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		source.Name,
		string(storage.SourceStatusIdle),
		`action="/sources/` + source.ID + `/run"`,
		`method="post"`,
		`name="search_query"`,
		`type="search"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sources does not contain %q: %s", want, body)
		}
	}
}

func TestManualSourceRunForwardsSubmittedSearchQuery(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	browser := &queryRecordingBrowser{}
	handler := New(store, dryrun.New(browser, time.Minute), browser)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")

	request := httptest.NewRequest(http.MethodPost, "/sources/"+source.ID+"/run", strings.NewReader("search_query=Go+developer"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /sources/:id/run = %d: %s", recorder.Code, recorder.Body.String())
	}
	if browser.query != "Go developer" {
		t.Errorf("ScrapeWithQuery query = %q, want submitted phrase", browser.query)
	}
	if browser.usedLegacyScrape {
		t.Error("manual run with a search phrase used legacy Scrape instead of ScrapeWithQuery")
	}
}

func TestScrapeSourceWithQueryUsesLegacyScrapeOnlyForBlankPhrase(t *testing.T) {
	t.Parallel()
	browser := &queryRecordingBrowser{}
	source := config.Source{}

	if _, err := scrapeSourceWithQuery(context.Background(), browser, source, " \t "); err != nil {
		t.Fatalf("scrapeSourceWithQuery blank query: %v", err)
	}
	if !browser.usedLegacyScrape || browser.query != "" {
		t.Errorf("blank query calls = legacy:%t query:%q, want legacy only", browser.usedLegacyScrape, browser.query)
	}
}

func TestScrapeSourceWithQueryRejectsPhraseForLegacyOnlyBrowser(t *testing.T) {
	t.Parallel()
	if _, err := scrapeSourceWithQuery(context.Background(), fakeBrowser{}, config.Source{}, "Go"); err == nil {
		t.Fatal("scrapeSourceWithQuery() succeeded for a browser without query support")
	}
}

func TestManualSourceRunSavesResultAndRedirectsToFilteredDashboard(t *testing.T) {
	handler, store := sourceBuilderHandler(t)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sources/"+source.ID+"/run", nil))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("POST /sources/:id/run = %d: %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/?source_id="+source.ID {
		t.Errorf("POST /sources/:id/run Location = %q, want %q", location, "/?source_id="+source.ID)
	}
	runs, err := store.LatestRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v, want one", runs)
	}
	if runs[0].Status != storage.RunStatusSuccess {
		t.Errorf("run status = %q, want %q", runs[0].Status, storage.RunStatusSuccess)
	}
	vacancies, total, err := store.ListVacancies(context.Background(), storage.VacancyFilter{SourceID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(vacancies) != 1 || vacancies[0].Title != "Role" {
		t.Errorf("persisted vacancies = total %d, items %#v; want scraped Role", total, vacancies)
	}
}

func TestManualSourceRunUsesIncompleteHeadRunsAndPeriodicCompleteReconciliation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	browser := &paginationRecordingBrowser{}
	handler := New(store, dryrun.New(browser, time.Minute), browser)
	const configuredMaxIterations = 8
	source, err := store.CreateSource(ctx, storage.NewSource{
		ID: "source-1", Slug: "example", Name: "Example jobs", IsActive: true,
		ConfigYAML: `site_name: Example
base_url: https://example.test/jobs
page:
  wait_for_selector: .jobs
  timeout_seconds: 1
  settle_delay_ms: 0
pagination:
  type: scroll_or_button
  max_attempts_without_new_data: 1
  max_iterations: 8
selectors:
  container: .jobs
  card: .job
  title: h2
  link: a
`,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed the source with a known vacancy. The first three manual runs only
	// cover the head of the listing and therefore must not archive this record
	// when the test scraper does not return it.
	seed, err := store.StartRun(ctx, storage.NewRun{ID: "seed", SourceID: source.ID, StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyObservedVacancies(ctx, seed.ID, []storage.ObservedVacancy{
		{Title: "Head vacancy", Link: "https://example.test/jobs/head"},
		{Title: "Deep vacancy", Link: "https://example.test/jobs/deep"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRun(ctx, storage.CompleteRun{
		ID: seed.ID, Status: storage.RunStatusSuccess, CompletionStatus: storage.CompletionStatusComplete, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("head run %d status = %d: %s", attempt+1, recorder.Code, recorder.Body.String())
		}
		runs, err := store.LatestRuns(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if runs[0].Status != storage.RunStatusPartial || runs[0].CompletionStatus != storage.CompletionStatusIncomplete {
			t.Errorf("head run %d = status %q, completion %q; want partial/incomplete", attempt+1, runs[0].Status, runs[0].CompletionStatus)
		}
	}

	var listingStatus string
	if err := store.DB.QueryRowContext(ctx, "SELECT listing_status FROM vacancies WHERE source_id = ? AND canonical_link = ?", source.ID, "https://example.test/jobs/deep").Scan(&listingStatus); err != nil {
		t.Fatal(err)
	}
	if listingStatus != "active" {
		t.Errorf("deep vacancy after head runs = %q, want active", listingStatus)
	}

	full := httptest.NewRecorder()
	handler.ServeHTTP(full, httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil))
	if full.Code != http.StatusOK {
		t.Fatalf("full reconciliation status = %d: %s", full.Code, full.Body.String())
	}
	runs, err := store.LatestRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != storage.RunStatusSuccess || runs[0].CompletionStatus != storage.CompletionStatusComplete {
		t.Errorf("full reconciliation = status %q, completion %q; want success/complete", runs[0].Status, runs[0].CompletionStatus)
	}
	if runs[0].ArchivedCount != 1 {
		t.Errorf("full reconciliation archived = %d, want 1", runs[0].ArchivedCount)
	}
	if err := store.DB.QueryRowContext(ctx, "SELECT listing_status FROM vacancies WHERE source_id = ? AND canonical_link = ?", source.ID, "https://example.test/jobs/deep").Scan(&listingStatus); err != nil {
		t.Fatal(err)
	}
	if listingStatus != "archived" {
		t.Errorf("deep vacancy after complete reconciliation = %q, want archived", listingStatus)
	}
	if got, want := browser.iterations, []int{3, 3, 3, configuredMaxIterations}; !slices.Equal(got, want) {
		t.Errorf("scraper pagination limits = %v, want %v", got, want)
	}
}

func TestManualSourceRunRejectsAlreadyRunningSourceWithoutSecondRun(t *testing.T) {
	ctx := context.Background()
	handler, store := sourceBuilderHandler(t)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")
	if _, err := store.StartExclusiveRun(ctx, storage.NewRun{ID: "running-1", SourceID: source.ID, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("start existing run: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sources/"+source.ID+"/run", nil))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST /sources/:id/run = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scraping_runs WHERE source_id = ?", source.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("run count after conflict = %d, want 1", count)
	}
}

func TestManualSourceRunDoesNotInheritCanceledHTTPRequestContext(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	browser := &contextRecordingBrowser{}
	handler := NewWithRunTimeout(store, dryrun.New(browser, time.Minute), browser, time.Minute)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil).WithContext(requestContext)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/sources/:id/run with canceled request = %d: %s", recorder.Code, recorder.Body.String())
	}
	if browser.contextErr != nil {
		t.Errorf("scraper context error = %v, want independent active context", browser.contextErr)
	}
	runs, err := store.LatestRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != storage.RunStatusSuccess {
		t.Errorf("runs after canceled request = %#v, want one successful run", runs)
	}
}

func TestManualSourceRunTimeoutReturnsGatewayTimeoutAndPersistsDiagnostic(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	browser := &deadlineWaitingBrowser{}
	handler := NewWithRunTimeout(store, dryrun.New(browser, time.Minute), browser, 10*time.Millisecond)
	source := createHTMLRunSource(t, store, "source-1", "example", "Example jobs")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil))

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("POST /api/sources/:id/run after deadline = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}
	runs, err := store.LatestRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after timeout = %#v, want one", runs)
	}
	if runs[0].Status != storage.RunStatusFailed || !strings.Contains(runs[0].ErrorMessage, context.DeadlineExceeded.Error()) {
		t.Errorf("timed out run = %#v, want failed diagnostic containing %q", runs[0], context.DeadlineExceeded)
	}
	updatedSource, err := store.GetSource(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedSource.Status != storage.SourceStatusFailed || !strings.Contains(updatedSource.LastError, context.DeadlineExceeded.Error()) {
		t.Errorf("timed out source = %#v, want failed diagnostic containing %q", updatedSource, context.DeadlineExceeded)
	}
}

func createHTMLRunSource(t *testing.T, store *storage.Store, id, slug, name string) storage.Source {
	t.Helper()
	source, err := store.CreateSource(context.Background(), storage.NewSource{
		ID: id, Slug: slug, Name: name, ConfigYAML: string(validYAML()), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

type queryRecordingBrowser struct {
	query            string
	usedLegacyScrape bool
}

type paginationRecordingBrowser struct {
	iterations []int
}

func (b *paginationRecordingBrowser) Scrape(_ context.Context, source config.Source) (scraper.Result, error) {
	b.iterations = append(b.iterations, source.Pagination.MaxIterations)
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Head vacancy", Link: "https://example.test/jobs/head"}}}, nil
}

type contextRecordingBrowser struct {
	contextErr error
}

func (b *contextRecordingBrowser) Scrape(ctx context.Context, _ config.Source) (scraper.Result, error) {
	b.contextErr = ctx.Err()
	if err := ctx.Err(); err != nil {
		return scraper.Result{}, err
	}
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}

type deadlineWaitingBrowser struct{}

func (deadlineWaitingBrowser) Scrape(ctx context.Context, _ config.Source) (scraper.Result, error) {
	<-ctx.Done()
	return scraper.Result{}, ctx.Err()
}

func (b *queryRecordingBrowser) Scrape(context.Context, config.Source) (scraper.Result, error) {
	b.usedLegacyScrape = true
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}

func (b *queryRecordingBrowser) ScrapeWithQuery(_ context.Context, _ config.Source, query string) (scraper.Result, error) {
	b.query = query
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}
