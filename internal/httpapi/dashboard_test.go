package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/storage"
)

func TestDashboardRendersVacancySourceRunSummaryAndLocalStylesheet(t *testing.T) {
	handler := dashboardHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", contentType)
	}
	for _, want := range []string{
		"Go developer", "Example source", "120 000", "https://example.test/jobs/1",
		"success", "+3 новых · 2 изм. · 0 архив.", `href="/static/app.css"`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("dashboard does not render %q: %s", want, response.Body.String())
		}
	}
}

func TestDashboardRendersRunButtonsForIdleAndRunningSources(t *testing.T) {
	ctx := context.Background()
	handler, store := sourceBuilderHandler(t)
	idle := createHTMLRunSource(t, store, "idle-source", "idle", "Idle source")
	running := createHTMLRunSource(t, store, "running-source", "running", "Running source")
	if _, err := store.StartExclusiveRun(ctx, storage.NewRun{
		ID: "running-source-run", SourceID: running.ID, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("start running source: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{idle.Name, string(storage.SourceStatusIdle), running.Name, string(storage.SourceStatusRunning)} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard does not render source state %q: %s", want, body)
		}
	}

	idleForm := dashboardSourceRunForm(t, body, idle.ID)
	if !regexp.MustCompile(`<button[^>]*type="submit"[^>]*>\s*Запустить\s*</button>`).MatchString(idleForm) {
		t.Errorf("idle source form has no visible submit button: %s", idleForm)
	}
	if regexp.MustCompile(`<button[^>]*\sdisabled(?:[\s=>]|$)`).MatchString(idleForm) {
		t.Errorf("idle source form unexpectedly disables run button: %s", idleForm)
	}

	runningForm := dashboardSourceRunForm(t, body, running.ID)
	if !regexp.MustCompile(`<button[^>]*\sdisabled(?:[\s=>]|$)[^>]*>\s*Запустить\s*</button>`).MatchString(runningForm) {
		t.Errorf("running source form does not disable run button: %s", runningForm)
	}
}

func dashboardSourceRunForm(t *testing.T, body, sourceID string) string {
	t.Helper()
	pattern := `(?s)<form[^>]*action="/sources/` + regexp.QuoteMeta(sourceID) + `/run"[^>]*>.*?</form>`
	form := regexp.MustCompile(pattern).FindString(body)
	if form == "" {
		t.Fatalf("dashboard has no POST run form for source %q: %s", sourceID, body)
	}
	if !strings.Contains(form, `method="post"`) {
		t.Errorf("dashboard run form for source %q does not use POST: %s", sourceID, form)
	}
	return form
}

func TestDashboardUserStatusFilterLimitsVacanciesAndKeepsSelection(t *testing.T) {
	handler := dashboardHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?user_status=viewed", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET filtered dashboard = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Viewed role") {
		t.Errorf("filtered dashboard does not contain viewed vacancy: %s", body)
	}
	if strings.Contains(body, "Go developer") {
		t.Errorf("filtered dashboard contains vacancy outside user_status=viewed: %s", body)
	}
	selectedViewed := regexp.MustCompile(`<option[^>]*(value="viewed"[^>]*selected|selected[^>]*value="viewed")[^>]*>`)
	if !selectedViewed.MatchString(body) {
		t.Errorf("dashboard does not preserve user_status=viewed as selected: %s", body)
	}
}

func TestDashboardStylesheetIsLocalAndDashboardHasNoCDNURLs(t *testing.T) {
	handler := dashboardHandler(t)

	dashboard := httptest.NewRecorder()
	handler.ServeHTTP(dashboard, httptest.NewRequest(http.MethodGet, "/", nil))
	if dashboard.Code != http.StatusOK {
		t.Fatalf("GET / = %d: %s", dashboard.Code, dashboard.Body.String())
	}

	stylesheet := httptest.NewRecorder()
	handler.ServeHTTP(stylesheet, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if stylesheet.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css = %d: %s", stylesheet.Code, stylesheet.Body.String())
	}
	if contentType := stylesheet.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/css") {
		t.Errorf("stylesheet Content-Type = %q, want text/css", contentType)
	}

	cdnURL := regexp.MustCompile(`https?://[^\"'[:space:]]*(cdn|unpkg|jsdelivr|cdnjs|fonts\.googleapis)\.`)
	for name, body := range map[string]string{"dashboard": dashboard.Body.String(), "stylesheet": stylesheet.Body.String()} {
		if match := cdnURL.FindString(body); match != "" {
			t.Errorf("%s contains CDN URL %q", name, match)
		}
	}
}

func dashboardHandler(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.CreateSource(ctx, storage.NewSource{
		ID: "dashboard-source", Slug: "example", Name: "Example source", ConfigYAML: string(validYAML()), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, storage.NewRun{ID: "dashboard-run", SourceID: source.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyObservedVacancies(ctx, run.ID, []storage.ObservedVacancy{
		{Title: "Go developer", Company: "Example", Salary: "120 000", Link: "https://example.test/jobs/1"},
		{Title: "Viewed role", Company: "Example", Link: "https://example.test/jobs/2"},
	}); err != nil {
		t.Fatal(err)
	}
	items, _, err := store.ListVacancies(ctx, storage.VacancyFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Title == "Viewed role" {
			if _, err := store.UpdateVacancyUserStatus(ctx, item.ID, "viewed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.CompleteRun(ctx, storage.CompleteRun{
		ID: run.ID, Status: storage.RunStatusSuccess, CompletionStatus: storage.CompletionStatusComplete,
		FinishedAt: time.Now(), AddedCount: 3, UpdatedCount: 2, SeenCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	return New(store, dryrun.New(fakeBrowser{}, time.Minute), fakeBrowser{})
}
