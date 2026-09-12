package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func TestTestConfigThenCreateSourceRequiresMatchingToken(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := dryrun.New(fakeBrowser{}, time.Minute)
	handler := New(store, service, fakeBrowser{})
	yaml := `site_name: Example
base_url: https://example.test/jobs
page:
  wait_for_selector: .jobs
  timeout_seconds: 1
  settle_delay_ms: 0
selectors:
  container: .jobs
  card: .job
  title: h2
  link: a
`
	test := httptest.NewRequest(http.MethodPost, "/api/sources/test-config", bytes.NewBufferString(`{"yaml":`+quote(yaml)+`}`))
	testResult := httptest.NewRecorder()
	handler.ServeHTTP(testResult, test)
	if testResult.Code != http.StatusOK {
		t.Fatalf("dry run status = %d: %s", testResult.Code, testResult.Body.String())
	}
	var response struct {
		Token string `json:"test_token"`
	}
	if err := json.Unmarshal(testResult.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/sources", bytes.NewBufferString(`{"slug":"example","yaml":`+quote(yaml)+`,"test_token":`+quote(response.Token)+`}`))
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", created.Code, created.Body.String())
	}
	var source storage.Source
	if err := json.Unmarshal(created.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	run := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil)
	runResult := httptest.NewRecorder()
	handler.ServeHTTP(runResult, run)
	if runResult.Code != http.StatusOK {
		t.Fatalf("run status = %d: %s", runResult.Code, runResult.Body.String())
	}
}

func TestReadEndpointsAndVacancyStatus(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := store.CreateSource(ctx, storage.NewSource{ID: "source-1", Slug: "example", Name: "Example", ConfigYAML: string(validYAML()), IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, storage.NewRun{ID: "run-1", SourceID: source.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyObservedVacancies(ctx, run.ID, []storage.ObservedVacancy{{Title: "Go developer", Company: "Example", Link: "https://example.test/1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRun(ctx, storage.CompleteRun{ID: run.ID, Status: storage.RunStatusSuccess, CompletionStatus: storage.CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	handler := New(store, dryrun.New(fakeBrowser{}, time.Minute), fakeBrowser{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/vacancies?search=go&run_id=run-1", nil))
	if response.Code != 200 {
		t.Fatalf("vacancies = %d: %s", response.Code, response.Body.String())
	}
	var listed struct {
		Items []storage.Vacancy `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("items = %#v", listed.Items)
	}
	patched := httptest.NewRecorder()
	handler.ServeHTTP(patched, httptest.NewRequest(http.MethodPatch, "/api/vacancies/"+listed.Items[0].ID, bytes.NewBufferString(`{"user_status":"viewed"}`)))
	if patched.Code != 200 {
		t.Fatalf("patch=%d: %s", patched.Code, patched.Body.String())
	}
	for _, path := range []string{"/api/sources", "/api/sources/source-1/export", "/api/runs/latest"} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != 200 {
			t.Errorf("GET %s = %d: %s", path, r.Code, r.Body.String())
		}
	}
	imported := httptest.NewRecorder()
	handler.ServeHTTP(imported, httptest.NewRequest(http.MethodPost, "/api/sources/import", bytes.NewBufferString(`{"yaml":`+quote(string(validYAML()))+`}`)))
	if imported.Code != 200 || !bytes.Contains(imported.Body.Bytes(), []byte("requires_dry_run")) {
		t.Errorf("import = %d: %s", imported.Code, imported.Body.String())
	}
}

func TestReadResponsesUseSnakeCaseAndEffectivePagination(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := store.CreateSource(ctx, storage.NewSource{ID: "source-1", Slug: "example", Name: "Example", ConfigYAML: string(validYAML()), IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, storage.NewRun{ID: "run-1", SourceID: source.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyObservedVacancies(ctx, run.ID, []storage.ObservedVacancy{{Title: "Go developer", Company: "Example", Link: "https://example.test/1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteRun(ctx, storage.CompleteRun{ID: run.ID, Status: storage.RunStatusSuccess, CompletionStatus: storage.CompletionStatusComplete, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	handler := New(store, dryrun.New(fakeBrowser{}, time.Minute), fakeBrowser{})

	assertSnakeCase := func(path string, required, forbidden []string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		for _, key := range required {
			if _, ok := body[key]; !ok {
				t.Errorf("GET %s response missing JSON key %q: %s", path, key, recorder.Body.String())
			}
		}
		for _, key := range forbidden {
			if _, ok := body[key]; ok {
				t.Errorf("GET %s response contains Go-style JSON key %q: %s", path, key, recorder.Body.String())
			}
		}
		return body
	}

	vacancies := assertSnakeCase("/api/vacancies?limit=500&offset=-5", []string{"items", "total", "limit", "offset"}, nil)
	if vacancies["limit"] != float64(50) || vacancies["offset"] != float64(0) {
		t.Errorf("effective pagination = limit=%v offset=%v, want 50 and 0", vacancies["limit"], vacancies["offset"])
	}
	items, ok := vacancies["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("vacancy items = %#v, want one item", vacancies["items"])
	}
	vacancy, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("vacancy item = %#v, want object", items[0])
	}
	for _, key := range []string{"source_id", "canonical_link", "listing_status", "user_status", "last_run_id"} {
		if _, ok := vacancy[key]; !ok {
			t.Errorf("vacancy missing JSON key %q: %#v", key, vacancy)
		}
	}
	for _, key := range []string{"SourceID", "CanonicalLink", "ListingStatus", "UserStatus", "LastRunID"} {
		if _, ok := vacancy[key]; ok {
			t.Errorf("vacancy contains Go-style JSON key %q: %#v", key, vacancy)
		}
	}

	sourceRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sourceRecorder, httptest.NewRequest(http.MethodGet, "/api/sources", nil))
	var listedSources []map[string]any
	if err := json.Unmarshal(sourceRecorder.Body.Bytes(), &listedSources); err != nil {
		t.Fatalf("decode sources: %v", err)
	}
	if len(listedSources) != 1 {
		t.Fatalf("sources = %#v, want one source", listedSources)
	}
	for _, key := range []string{"config_yaml", "is_active", "schedule_type", "schedule_value", "schedule_timezone", "last_run_at", "last_error", "created_at", "updated_at"} {
		if _, ok := listedSources[0][key]; !ok {
			t.Errorf("source missing JSON key %q: %#v", key, listedSources[0])
		}
	}
	if _, ok := listedSources[0]["ConfigYAML"]; ok {
		t.Errorf("source contains Go-style ConfigYAML: %#v", listedSources[0])
	}

	runsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(runsRecorder, httptest.NewRequest(http.MethodGet, "/api/runs/latest", nil))
	var runs []map[string]any
	if err := json.Unmarshal(runsRecorder.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v, want one run", runs)
	}
	for _, key := range []string{"source_id", "started_at", "finished_at", "completion_status", "added_count", "updated_count", "archived_count", "seen_count", "error_message", "meta_json"} {
		if _, ok := runs[0][key]; !ok {
			t.Errorf("run missing JSON key %q: %#v", key, runs[0])
		}
	}
	if _, ok := runs[0]["SourceID"]; ok {
		t.Errorf("run contains Go-style SourceID: %#v", runs[0])
	}
}

func TestRunFailureWhileFinishingMarksRunFailedAndUnlocksSource(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := store.CreateSource(ctx, storage.NewSource{ID: "source-1", Slug: "example", Name: "Example", ConfigYAML: string(validYAML()), IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`CREATE TRIGGER reject_vacancy_insert BEFORE INSERT ON vacancies BEGIN SELECT RAISE(ABORT, 'forced result persistence failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	handler := New(store, dryrun.New(fakeBrowser{}, time.Minute), fakeBrowser{})

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first run = %d: %s", first.Code, first.Body.String())
	}
	var runStatus string
	if err := store.DB.QueryRowContext(ctx, "SELECT status FROM scraping_runs WHERE source_id = ?", source.ID).Scan(&runStatus); err != nil {
		t.Fatalf("read failed run status: %v", err)
	}
	if runStatus != string(storage.RunStatusFailed) {
		t.Errorf("run status after Finish error = %q, want failed", runStatus)
	}
	updatedSource, err := store.GetSource(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedSource.Status != storage.SourceStatusFailed {
		t.Errorf("source status after Finish error = %q, want failed", updatedSource.Status)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/run", nil))
	if second.Code == http.StatusConflict {
		t.Errorf("second run = 409: %s; source was not unlocked after Finish error", second.Body.String())
	}
}

type fakeBrowser struct{}

func (fakeBrowser) Scrape(context.Context, config.Source) (scraper.Result, error) {
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}
func quote(value string) string { bytes, _ := json.Marshal(value); return string(bytes) }
func validYAML() []byte {
	return []byte("site_name: Example\nbase_url: https://example.test/jobs\npage:\n  wait_for_selector: .jobs\n  timeout_seconds: 1\n  settle_delay_ms: 0\nselectors:\n  container: .jobs\n  card: .job\n  title: h2\n  link: a\n")
}
