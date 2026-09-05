// Package runner persists the lifecycle around one scraper invocation.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/storage"
)

// Service coordinates source registration and the persistent states of a run.
type Service struct {
	store *storage.Store
	newID func() string
}

// Started identifies the source and running database record created before Chromium starts.
type Started struct {
	Source storage.Source
	Run    storage.Run
}

// New creates a lifecycle service backed by store.
func New(store *storage.Store) Service {
	return Service{store: store, newID: uuid.NewString}
}

// Start registers or updates the manual source and records a running attempt.
func (s Service) Start(ctx context.Context, input config.Source, configYAML string) (Started, error) {
	if s.store == nil {
		return Started{}, fmt.Errorf("storage is required")
	}
	if strings.TrimSpace(configYAML) == "" {
		return Started{}, fmt.Errorf("source YAML is required")
	}

	slug := sourceSlug(input.SiteName)
	source, err := s.store.GetSourceBySlug(ctx, slug)
	if errors.Is(err, storage.ErrNotFound) {
		source, err = s.store.CreateSource(ctx, storage.NewSource{
			ID:         s.newID(),
			Slug:       slug,
			Name:       input.SiteName,
			ConfigYAML: configYAML,
			IsActive:   true,
		})
	} else if err == nil {
		// Operational fields (active flag and future schedule) belong to the
		// source record, so a CLI run changes only its current configuration.
		source.Name = input.SiteName
		source.ConfigYAML = configYAML
		source, err = s.store.UpdateSource(ctx, source)
	}
	if err != nil {
		return Started{}, fmt.Errorf("ensure source %q: %w", input.SiteName, err)
	}

	run, err := s.store.StartRun(ctx, storage.NewRun{ID: s.newID(), SourceID: source.ID, StartedAt: time.Now().UTC()})
	if err != nil {
		return Started{}, fmt.Errorf("start source run: %w", err)
	}
	return Started{Source: source, Run: run}, nil
}

func sourceSlug(siteName string) string {
	var value strings.Builder
	separator := true
	for _, char := range strings.ToLower(strings.TrimSpace(siteName)) {
		if char <= unicode.MaxASCII && (unicode.IsLetter(char) || unicode.IsDigit(char)) {
			value.WriteRune(char)
			separator = false
		} else if !separator {
			value.WriteByte('-')
			separator = true
		}
	}
	base := strings.Trim(value.String(), "-")
	if base == "" {
		base = "source"
	}
	// The suffix makes distinct display names safe even if their ASCII slugs
	// coincide, while retaining a readable part for diagnostics and future UI.
	sum := sha256.Sum256([]byte(strings.TrimSpace(siteName)))
	return base + "-" + hex.EncodeToString(sum[:6])
}
