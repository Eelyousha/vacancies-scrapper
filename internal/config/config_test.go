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
