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

type fakeBrowser struct{}

func (fakeBrowser) Scrape(context.Context, config.Source) (scraper.Result, error) {
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}
func quote(value string) string { bytes, _ := json.Marshal(value); return string(bytes) }
func validYAML() []byte {
	return []byte("site_name: Example\nbase_url: https://example.test/jobs\npage:\n  wait_for_selector: .jobs\n  timeout_seconds: 1\n  settle_delay_ms: 0\nselectors:\n  container: .jobs\n  card: .job\n  title: h2\n  link: a\n")
}
