package config

import (
	"strings"
	"testing"
)

func TestValidateIdentityFallbackFields(t *testing.T) {
	base := validSource()
	for name, identity := range map[string]*Identity{
		"empty":     {FallbackFields: nil},
		"unknown":   {FallbackFields: []string{"location"}},
		"duplicate": {FallbackFields: []string{"title", "title"}},
		"link":      {FallbackFields: []string{"link"}},
	} {
		t.Run(name, func(t *testing.T) {
			source := base
			source.Identity = identity
			if err := source.Validate(); err == nil {
				t.Fatal("Validate() succeeded for invalid fallback fields")
			}
		})
	}
}

func TestValidateAcceptsKnownIdentityFallbackFields(t *testing.T) {
	source := validSource()
	source.Identity = &Identity{FallbackFields: []string{"title", "company", "description"}}
	if err := source.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}

func validSource() Source {
	return Source{
		SiteName: "Example", BaseURL: "https://example.test/jobs",
		Page:      Page{WaitForSelector: ".jobs", TimeoutSeconds: 1},
		Selectors: Selectors{Container: ".jobs", Card: ".job", Title: "h2", Link: "a"},
	}
}

func TestValidateIdentityErrorNamesField(t *testing.T) {
	source := validSource()
	source.Identity = &Identity{FallbackFields: []string{"unknown"}}
	err := source.Validate()
	if err == nil || !strings.Contains(err.Error(), "identity.fallback_fields") {
		t.Errorf("Validate() error = %v, want identity.fallback_fields context", err)
	}
}

func TestValidateAcceptsValidCSSSelectors(t *testing.T) {
	source := validSource()
	source.Selectors = Selectors{
		Container:   "main.jobs > ul[data-kind='vacancy']",
		Card:        "li.job-card:nth-child(2n+1)",
		Title:       "h2 a[href^='/jobs/']",
		Company:     ".company-name",
		Salary:      "[data-salary]",
		Link:        "a[href]",
		Description: ".description > p:first-child",
	}
	source.Pagination = &Pagination{
		Type:                      "scroll_or_button",
		ActionSelector:            "button.load-more:not([disabled])",
		MaxAttemptsWithoutNewData: 1,
		MaxIterations:             1,
		StopConditions: StopConditions{
			NoMoreResultsSelector:  ".empty-state",
			ButtonDisabledSelector: "button.load-more[disabled]",
		},
	}
	source.DetailPage = &DetailPage{
		WaitForSelector: "article.job-detail",
		TimeoutSeconds:  1,
		Selectors: DetailSelectors{
			Title:       "h1",
			Company:     ".company",
			Salary:      ".salary",
			Description: "section.description",
		},
	}
	source.SearchOnUI = &SearchOnUI{
		InputSelector:  "input[name='query']",
		SubmitSelector: "button[type='submit']",
	}

	if err := source.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want valid CSS selectors to pass", err)
	}
}

func TestValidateRejectsInvalidCSSSelectorWithYAMLPath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		apply func(*Source)
	}{
		{
			name: "required list selector",
			path: "selectors.container",
			apply: func(source *Source) {
				source.Selectors.Container = "["
			},
		},
		{
			name: "optional list selector",
			path: "selectors.company",
			apply: func(source *Source) {
				source.Selectors.Company = "["
			},
		},
		{
			name: "pagination action selector",
			path: "pagination.action_selector",
			apply: func(source *Source) {
				source.Pagination = validPagination()
				source.Pagination.ActionSelector = "["
			},
		},
		{
			name: "pagination no more results selector",
			path: "pagination.stop_conditions.no_more_results_selector",
			apply: func(source *Source) {
				source.Pagination = validPagination()
				source.Pagination.StopConditions.NoMoreResultsSelector = "["
			},
		},
		{
			name: "pagination disabled button selector",
			path: "pagination.stop_conditions.button_disabled_selector",
			apply: func(source *Source) {
				source.Pagination = validPagination()
				source.Pagination.StopConditions.ButtonDisabledSelector = "["
			},
		},
		{
			name: "detail page wait selector",
			path: "detail_page.wait_for_selector",
			apply: func(source *Source) {
				source.DetailPage = validDetailPage()
				source.DetailPage.WaitForSelector = "["
			},
		},
		{
			name: "detail page field selector",
			path: "detail_page.selectors.title",
			apply: func(source *Source) {
				source.DetailPage = validDetailPage()
				source.DetailPage.Selectors.Title = "["
			},
		},
		{
			name: "search input selector",
			path: "search_on_ui.input_selector",
			apply: func(source *Source) {
				source.SearchOnUI = &SearchOnUI{InputSelector: "[", Enter: true}
			},
		},
		{
			name: "search submit selector",
			path: "search_on_ui.submit_selector",
			apply: func(source *Source) {
				source.SearchOnUI = &SearchOnUI{InputSelector: "input", SubmitSelector: "["}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validSource()
			test.apply(&source)

			err := source.Validate()
			if err == nil {
				t.Fatal("Validate() succeeded for invalid CSS selector")
			}
			if !strings.Contains(err.Error(), test.path) {
				t.Errorf("Validate() error = %v, want YAML path %q", err, test.path)
			}
		})
	}
}

func validPagination() *Pagination {
	return &Pagination{
		Type:                      "scroll_or_button",
		MaxAttemptsWithoutNewData: 1,
		MaxIterations:             1,
	}
}

func validDetailPage() *DetailPage {
	return &DetailPage{
		WaitForSelector: "main",
		TimeoutSeconds:  1,
		Selectors:       DetailSelectors{Title: "h1"},
	}
}
