package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"vacancies-scrapper/internal/config"
	"vacancies-scrapper/internal/scraper"
)

func main() {
	// Параметры командной строки намеренно не меняют YAML-файл. Это позволяет
	// безопасно сокращать пробный запуск, не теряя проверенную конфигурацию.
	configPath := flag.String("config", "yandex_vacancies.yaml", "path to source YAML configuration")
	maxResults := flag.Int("max-results", 0, "maximum number of extracted vacancies (0 means all)")
	maxDetailPages := flag.Int("max-detail-pages", 0, "maximum number of detail pages to open (0 means all)")
	maxIterations := flag.Int("max-pagination-iterations", 0, "override pagination iterations for this run (0 uses YAML)")
	searchQuery := flag.String("search", "", "optional search phrase for this manual run")
	headless := flag.Bool("headless", true, "run Chromium headlessly")
	runTimeout := flag.Duration("run-timeout", 5*time.Minute, "maximum duration of the complete scrape")
	flag.Parse()

	sourceConfig, err := config.Load(*configPath)
	if err != nil {
		fail(err)
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
	result, err := scraper.New(*headless, *maxDetailPages).ScrapeWithQuery(ctx, sourceConfig, *searchQuery)
	if err != nil {
		fail(err)
	}

	// Результат ограничивается после извлечения: это не меняет алгоритм
	// пагинации и делает CLI удобным для короткой проверки селекторов.
	if *maxResults > 0 && len(result.Vacancies) > *maxResults {
		result.Vacancies = result.Vacancies[:*maxResults]
	}

	result.FinishedAt = time.Now().UTC()
	// JSON предназначен для человека и дальнейшей автоматической обработки.
	// SetEscapeHTML сохраняет кириллицу и URL в читаемом виде.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fail(fmt.Errorf("encode result: %w", err))
	}
}

// fail печатает фатальную ошибку в stderr и завершает CLI с ненулевым кодом.
func fail(err error) {
	// Все ошибки CLI выводятся в stderr, чтобы stdout оставался валидным JSON
	// при успешном запуске и мог быть перенаправлен в файл или другой процесс.
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
