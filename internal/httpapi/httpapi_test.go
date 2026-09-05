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

type fakeBrowser struct{}

func (fakeBrowser) Scrape(context.Context, config.Source) (scraper.Result, error) {
	return scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}, nil
}
func quote(value string) string { bytes, _ := json.Marshal(value); return string(bytes) }
