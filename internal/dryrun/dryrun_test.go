package dryrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/scraper"
)

func TestTestIssuesOneTimeTokenForUsablePreview(t *testing.T) {
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	service := New(fakeScraper{result: scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}}, time.Minute)
	service.now = func() time.Time { return now }
	yamlSource := validYAML()

	result, err := service.Test(context.Background(), yamlSource)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if len(result.Preview) != 1 || result.Token == "" || !result.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Errorf("result = %#v, want preview and minute token", result)
	}
	if err := service.ConsumeToken(yamlSource, "https://example.test/jobs", result.Token); err != nil {
		t.Fatalf("ConsumeToken(): %v", err)
	}
	if err := service.ConsumeToken(yamlSource, "https://example.test/jobs", result.Token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("second ConsumeToken() error = %v, want ErrInvalidToken", err)
	}
}

func TestTestRejectsEmptyPreviewAndTokenMismatch(t *testing.T) {
	yamlSource := validYAML()
	service := New(fakeScraper{result: scraper.Result{Vacancies: []scraper.Vacancy{{Title: "", Link: ""}}}}, time.Minute)
	if _, err := service.Test(context.Background(), yamlSource); err == nil {
		t.Fatal("Test() succeeded without usable vacancy")
	}

	service = New(fakeScraper{result: scraper.Result{Vacancies: []scraper.Vacancy{{Title: "Role", Link: "https://example.test/jobs/1"}}}}, time.Minute)
	result, err := service.Test(context.Background(), yamlSource)
	if err != nil {
		t.Fatalf("Test(): %v", err)
	}
	if err := service.ConsumeToken([]byte("different"), "https://example.test/jobs", result.Token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("mismatched token error = %v, want ErrInvalidToken", err)
	}
}

type fakeScraper struct{ result scraper.Result }

func (f fakeScraper) Scrape(context.Context, config.Source) (scraper.Result, error) {
	return f.result, nil
}

func validYAML() []byte {
	return []byte("site_name: Example\nbase_url: https://example.test/jobs\npage:\n  wait_for_selector: .jobs\n  timeout_seconds: 1\n  settle_delay_ms: 0\nselectors:\n  container: .jobs\n  card: .job\n  title: h2\n  link: a\n")
}
