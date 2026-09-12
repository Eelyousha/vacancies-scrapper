package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
