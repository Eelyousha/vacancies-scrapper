package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/storage"
)

func TestSourceBuilderFormUsesLocalStylesheet(t *testing.T) {
	handler, _ := sourceBuilderHandler(t)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sources", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /sources = %d: %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("GET /sources Content-Type = %q, want text/html", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`href="/static/app.css"`, `action="/sources/test"`, `method="post"`,
		`name="slug"`, `name="name"`, `name="yaml"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source builder form does not contain %q: %s", want, body)
		}
	}
}

func TestSourceBuilderDryRunRendersPreviewAndHiddenToken(t *testing.T) {
	handler, _ := sourceBuilderHandler(t)

	recorder := serveSourceBuilderForm(handler, "/sources/test", url.Values{
		"slug": {"example"}, "name": {"Example source"}, "yaml": {string(validYAML())},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /sources/test = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Role") {
		t.Errorf("dry run preview is missing scraped vacancy: %s", body)
	}
	token := sourceBuilderToken(t, body)
	if token == "" {
		t.Error("dry run returned an empty test_token")
	}
	for _, want := range []string{`name="slug"`, `name="name"`, `name="yaml"`, `action="/sources"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dry run form does not preserve field %q: %s", want, body)
		}
	}
}

func TestSourceBuilderCreateWithMatchingTokenRedirectsToDashboard(t *testing.T) {
	handler, store := sourceBuilderHandler(t)
	yaml := string(validYAML())
	tested := serveSourceBuilderForm(handler, "/sources/test", url.Values{"slug": {"example"}, "name": {"Example source"}, "yaml": {yaml}})
	if tested.Code != http.StatusOK {
		t.Fatalf("POST /sources/test = %d: %s", tested.Code, tested.Body.String())
	}

	created := serveSourceBuilderForm(handler, "/sources", url.Values{
		"slug": {"example"}, "name": {"Example source"}, "yaml": {yaml}, "test_token": {sourceBuilderToken(t, tested.Body.String())},
	})
	if created.Code != http.StatusSeeOther {
		t.Fatalf("POST /sources = %d: %s", created.Code, created.Body.String())
	}
	if location := created.Header().Get("Location"); location != "/" {
		t.Errorf("POST /sources Location = %q, want dashboard /", location)
	}
	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Slug != "example" || sources[0].Name != "Example source" {
		t.Errorf("saved sources = %#v, want one submitted source", sources)
	}
}

func TestSourceBuilderRejectsMissingTokenOrChangedYAMLWithoutWriting(t *testing.T) {
	handler, store := sourceBuilderHandler(t)
	yaml := string(validYAML())

	for _, test := range []struct {
		name   string
		values url.Values
	}{
		{name: "missing token", values: url.Values{"slug": {"missing-token"}, "yaml": {yaml}}},
		{name: "changed yaml", values: url.Values{"slug": {"changed-yaml"}, "yaml": {yaml + "# changed after dry run\n"}, "test_token": {sourceBuilderDryRunToken(t, handler, yaml)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveSourceBuilderForm(handler, "/sources", test.values)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Errorf("POST /sources = %d, want 422: %s", recorder.Code, recorder.Body.String())
			}
			sources, err := store.ListSources(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(sources) != 0 {
				t.Errorf("POST /sources wrote sources on invalid token: %#v", sources)
			}
		})
	}
}

func TestSourceBuilderInvalidYAMLStaysInFormWithError(t *testing.T) {
	handler, store := sourceBuilderHandler(t)
	invalidYAML := "site_name: [\n"

	recorder := serveSourceBuilderForm(handler, "/sources/test", url.Values{
		"slug": {"broken"}, "name": {"Broken source"}, "yaml": {invalidYAML},
	})

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /sources/test = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "Ошибка") && !strings.Contains(strings.ToLower(body), "error") {
		t.Errorf("invalid YAML response has no form error: %s", body)
	}
	if !strings.Contains(body, "site_name: [") {
		t.Errorf("invalid YAML was not preserved in form: %s", body)
	}
	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Errorf("invalid YAML wrote sources: %#v", sources)
	}
}

func sourceBuilderHandler(t *testing.T) (http.Handler, *storage.Store) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vacancies.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store, dryrun.New(fakeBrowser{}, time.Minute), fakeBrowser{}), store
}

func serveSourceBuilderForm(handler http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func sourceBuilderDryRunToken(t *testing.T, handler http.Handler, yaml string) string {
	t.Helper()
	recorder := serveSourceBuilderForm(handler, "/sources/test", url.Values{"yaml": {yaml}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /sources/test = %d: %s", recorder.Code, recorder.Body.String())
	}
	return sourceBuilderToken(t, recorder.Body.String())
}

func sourceBuilderToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`<input[^>]+name="test_token"[^>]+value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("dry run response has no hidden test_token: %s", body)
	}
	return match[1]
}
