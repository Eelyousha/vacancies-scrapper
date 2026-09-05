// Package dryrun validates a source against a short browser collection before it is saved.
package dryrun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/scraper"
)

var ErrInvalidToken = errors.New("invalid or expired test token")

type Scraper interface {
	Scrape(context.Context, config.Source) (scraper.Result, error)
}

type Service struct {
	scraper Scraper
	tokens  *tokens
	now     func() time.Time
	ttl     time.Duration
}

type Result struct {
	Preview   []scraper.Vacancy `json:"preview"`
	Token     string            `json:"test_token"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func New(browser Scraper, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Service{scraper: browser, tokens: &tokens{values: make(map[string]token)}, now: time.Now, ttl: ttl}
}

func (s *Service) Test(ctx context.Context, yamlSource []byte) (Result, error) {
	if s.scraper == nil {
		return Result{}, fmt.Errorf("dry run scraper is required")
	}
	source, err := config.Parse(yamlSource)
	if err != nil {
		return Result{}, err
	}
	if source.Pagination != nil {
		pagination := *source.Pagination
		pagination.MaxIterations = 1
		source.Pagination = &pagination
	}
	// Preview must validate the listing itself; detail pages are not needed to
	// prove that the list selectors and links work.
	source.DetailPage = nil
	result, err := s.scraper.Scrape(ctx, source)
	if err != nil {
		return Result{}, fmt.Errorf("dry run scrape: %w", err)
	}
	preview := result.Vacancies
	if len(preview) > 5 {
		preview = preview[:5]
	}
	foundUsable := false
	for _, vacancy := range preview {
		if vacancy.Title != "" && vacancy.Link != "" {
			foundUsable = true
			break
		}
	}
	if !foundUsable {
		return Result{}, fmt.Errorf("dry run found no vacancy with title and link")
	}
	tokenValue, expiresAt, err := s.tokens.issue(yamlSource, source.BaseURL, s.now(), s.ttl)
	if err != nil {
		return Result{}, err
	}
	return Result{Preview: preview, Token: tokenValue, ExpiresAt: expiresAt}, nil
}

func (s *Service) ConsumeToken(yamlSource []byte, baseURL, value string) error {
	return s.tokens.consume(yamlSource, baseURL, value, s.now())
}

type token struct {
	hash      [32]byte
	baseURL   string
	expiresAt time.Time
}

type tokens struct {
	mu     sync.Mutex
	values map[string]token
}

func (t *tokens) issue(yamlSource []byte, baseURL string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", time.Time{}, fmt.Errorf("generate test token: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(bytes)
	expiresAt := now.Add(ttl)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.values[value] = token{hash: sha256.Sum256(yamlSource), baseURL: baseURL, expiresAt: expiresAt}
	return value, expiresAt, nil
}

func (t *tokens) consume(yamlSource []byte, baseURL, value string, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.values[value]
	delete(t.values, value)
	if !ok || now.After(entry.expiresAt) || entry.baseURL != baseURL || entry.hash != sha256.Sum256(yamlSource) {
		return ErrInvalidToken
	}
	return nil
}
