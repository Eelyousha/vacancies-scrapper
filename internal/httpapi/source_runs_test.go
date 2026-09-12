package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sources does not contain %q: %s", want, body)
		}
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
