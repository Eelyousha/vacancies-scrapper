package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/runlog"
	"vacancies-scrapper/internal/runner"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func main() {
	// Параметры командной строки намеренно не меняют YAML-файл. Это позволяет
	// безопасно сокращать пробный запуск, не теряя проверенную конфигурацию.
	configPath := flag.String("config", "yandex_vacancies.yaml", "path to source YAML configuration")
	databasePath := flag.String("database", "vacancies.db", "path to SQLite database")
	maxResults := flag.Int("max-results", 0, "maximum number of extracted vacancies (0 means all)")
	maxDetailPages := flag.Int("max-detail-pages", 0, "maximum number of detail pages to open (0 means all)")
	maxIterations := flag.Int("max-pagination-iterations", 0, "override pagination iterations for this run (0 uses YAML)")
	headless := flag.Bool("headless", true, "run Chromium headlessly")
	runTimeout := flag.Duration("run-timeout", 5*time.Minute, "maximum duration of the complete scrape")
	flag.Parse()

	sourceConfig, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	configYAML, err := os.ReadFile(*configPath)
	if err != nil {
		fail(fmt.Errorf("read source YAML for storage: %w", err))
	}
	// Ограничение итераций полезно при отладке нового сайта: парсер всё равно
	// извлечёт уже загруженные карточки, но не будет обходить всю выдачу.
	if *maxIterations > 0 && sourceConfig.Pagination != nil {
		sourceConfig.Pagination.MaxIterations = *maxIterations
	}

	// Один контекст задаёт верхнюю границу всей операции, включая запуск
	// Chromium, ожидание динамической загрузки и разбор итогового DOM.
	ctx, cancel := context.WithTimeout(context.Background(), *runTimeout)
	defer cancel()
	store, err := storage.Open(ctx, *databasePath)
	if err != nil {
		fail(err)
	}
	defer store.Close()
	lifecycle := runner.New(store)
	journal := runlog.New(store)
	started, err := lifecycle.Start(ctx, sourceConfig, string(configYAML))
	if err != nil {
		fail(err)
	}

	result, err := scraper.New(*headless, *maxDetailPages).Scrape(ctx, sourceConfig)
	if err != nil {
		if completionErr := recordFailure(journal, started.Run.ID, err); completionErr != nil {
			err = fmt.Errorf("%w; additionally could not save failed run: %v", err, completionErr)
		}
		fail(err)
	}

	// Результат ограничивается после извлечения: это не меняет алгоритм
	// пагинации и делает CLI удобным для короткой проверки селекторов.
	if *maxResults > 0 && len(result.Vacancies) > *maxResults {
		result.Vacancies = result.Vacancies[:*maxResults]
	}

	result.FinishedAt = time.Now().UTC()
	completion := storage.CompletionStatusComplete
	var fallbackFields []string
	if sourceConfig.Identity != nil {
		fallbackFields = sourceConfig.Identity.FallbackFields
	}
	// Diagnostic caps mean that the collected set may be deliberately incomplete.
	if *maxResults > 0 || *maxIterations > 0 {
		completion = storage.CompletionStatusIncomplete
	}
	if _, err := journal.Finish(ctx, started.Run.ID, result, fallbackFields, completion); err != nil {
		if completionErr := recordFailure(journal, started.Run.ID, err); completionErr != nil {
			err = fmt.Errorf("%w; additionally could not save failed run: %v", err, completionErr)
		}
		fail(err)
	}
	// JSON предназначен для человека и дальнейшей автоматической обработки.
	// SetEscapeHTML сохраняет кириллицу и URL в читаемом виде.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fail(fmt.Errorf("encode result: %w", err))
	}
}

func recordFailure(journal runlog.Logger, runID string, cause error) error {
	// Scrape commonly returns because its timeout cancelled ctx. A short detached
	// context still lets SQLite record that terminal failure instead of leaving a
	// permanently running run in the local history.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := journal.Fail(ctx, runID, cause.Error())
	return err
}

// fail печатает фатальную ошибку в stderr и завершает CLI с ненулевым кодом.
func fail(err error) {
	// Все ошибки CLI выводятся в stderr, чтобы stdout оставался валидным JSON
	// при успешном запуске и мог быть перенаправлен в файл или другой процесс.
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
