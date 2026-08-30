package scraper

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"

	"vacancies-scrapper/internal/config"
)

// Scraper управляет изолированным Chromium и преобразует отрисованный DOM
// в независимые от браузера структуры Vacancy.
type Scraper struct {
	headless       bool
	maxDetailPages int
}

// Result — отчёт одного запуска скрапера, который CLI сериализует в JSON.
// FinishedAt заполняется вызывающим кодом непосредственно перед выводом.
type Result struct {
	SiteName       string        `json:"site_name"`
	URL            string        `json:"url"`
	CardCount      int           `json:"card_count"`
	Iterations     int           `json:"pagination_iterations"`
	DetailsFetched int           `json:"details_fetched"`
	DetailErrors   []DetailError `json:"detail_errors,omitempty"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     time.Time     `json:"finished_at"`
	Vacancies      []Vacancy     `json:"vacancies"`
}

// Vacancy — минимальное представление карточки, извлечённое из списка вакансий.
// Поля Company, Salary и Description могут отсутствовать на конкретном сайте.
type Vacancy struct {
	Title       string `json:"title"`
	Company     string `json:"company,omitempty"`
	Salary      string `json:"salary,omitempty"`
	Link        string `json:"link"`
	Description string `json:"description,omitempty"`
}

// DetailError фиксирует ошибку отдельной вакансии, не отменяя результат всего
// списка. Позднее этот же принцип позволит помечать запуск как partial в БД.
type DetailError struct {
	Link  string `json:"link"`
	Error string `json:"error"`
}

// New создаёт скрапер с выбранным режимом отображения Chromium и лимитом
// детальных страниц. Нулевой лимит означает обход всех найденных ссылок.
func New(headless bool, maxDetailPages int) Scraper {
	// Режим headless отключает видимое окно Chromium и подходит для штатных запусков.
	// При отладке селекторов CLI передаёт false, чтобы показать действия браузера.
	return Scraper{headless: headless, maxDetailPages: maxDetailPages}
}

// Scrape открывает стартовую страницу, догружает карточки, получает HTML
// контейнера и разбирает его. Метод не пишет в БД и не меняет YAML-конфиг.
func (s Scraper) Scrape(ctx context.Context, source config.Source) (Result, error) {
	result := Result{SiteName: source.SiteName, URL: source.BaseURL, StartedAt: time.Now().UTC()}

	// Allocator запускает отдельный процесс Chromium. Его контекст и контекст
	// вкладки закрываются defer-ами даже при таймауте или ошибке парсинга.
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", s.headless))
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()

	// Poll использует внутренний timeout только для ожидания стартового элемента.
	// Отдельный context.WithTimeout здесь не применяется: отмена контекста chromedp
	// закрыла бы вкладку и прервала последующую пагинацию.
	if err := chromedp.Run(
		browserCtx,
		chromedp.Navigate(source.BaseURL),
		chromedp.Poll(
			"document.querySelector("+strconv.Quote(source.Page.WaitForSelector)+") !== null",
			nil,
			chromedp.WithPollingTimeout(source.Page.Timeout()),
		),
	); err != nil {
		return result, fmt.Errorf("open %s: %w", source.BaseURL, err)
	}

	iterations, err := loadAll(browserCtx, source)
	if err != nil {
		return result, err
	}
	result.Iterations = iterations

	// Передаём goquery только контейнер вакансий, а не весь документ. Это меньше
	// расходует память и исключает совпадения карточек из меню или футера.
	var html string
	if err := chromedp.Run(browserCtx, chromedp.OuterHTML(source.Selectors.Container, &html, chromedp.ByQuery)); err != nil {
		return result, fmt.Errorf("get vacancy container HTML: %w", err)
	}

	vacancies, err := parseHTML(html, source)
	if err != nil {
		return result, err
	}
	result.CardCount = len(vacancies)
	result.Vacancies = vacancies
	if source.DetailPage != nil {
		result.DetailsFetched, result.DetailErrors = enrichDetails(browserCtx, source, result.Vacancies, s.maxDetailPages)
	}
	return result, nil
}

// enrichDetails последовательно открывает ссылки вакансий и дополняет поля из
// списка. Последовательный режим намеренно снижает нагрузку на сайт и упрощает
// диагностику нестабильных селекторов; параллелизм будет добавлен с воркерами.
func enrichDetails(ctx context.Context, source config.Source, vacancies []Vacancy, maxPages int) (int, []DetailError) {
	detailPage := source.DetailPage
	if detailPage == nil {
		return 0, nil
	}

	fetched := 0
	var errors []DetailError
	for index := range vacancies {
		// Лимит используется только для диагностического запуска. Необойдённые
		// вакансии остаются в результате со значениями из карточки списка.
		if maxPages > 0 && index >= maxPages {
			break
		}
		if err := enrichVacancy(ctx, detailPage, &vacancies[index]); err != nil {
			errors = append(errors, DetailError{Link: vacancies[index].Link, Error: err.Error()})
			continue
		}
		fetched++
	}
	return fetched, errors
}

// enrichVacancy получает итоговый DOM детальной страницы и заменяет только
// непустые поля. Поэтому данные карточки списка остаются полезным fallback.
func enrichVacancy(ctx context.Context, detailPage *config.DetailPage, vacancy *Vacancy) error {
	if err := chromedp.Run(
		ctx,
		chromedp.Navigate(vacancy.Link),
		chromedp.Poll(
			"document.querySelector("+strconv.Quote(detailPage.WaitForSelector)+") !== null",
			nil,
			chromedp.WithPollingTimeout(time.Duration(detailPage.TimeoutSeconds)*time.Second),
		),
	); err != nil {
		return fmt.Errorf("open detail page: %w", err)
	}

	var html string
	if err := chromedp.Run(ctx, chromedp.OuterHTML("html", &html, chromedp.ByQuery)); err != nil {
		return fmt.Errorf("get detail page HTML: %w", err)
	}
	details, err := parseDetailHTML(html, detailPage.Selectors)
	if err != nil {
		return err
	}
	overwriteNonEmpty(vacancy, details)
	return nil
}

// parseDetailHTML извлекает настраиваемые поля из полного HTML детальной страницы.
func parseDetailHTML(html string, selectors config.DetailSelectors) (Vacancy, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return Vacancy{}, fmt.Errorf("parse detail HTML: %w", err)
	}
	return Vacancy{
		Title:       text(doc.Selection, selectors.Title),
		Company:     text(doc.Selection, selectors.Company),
		Salary:      text(doc.Selection, selectors.Salary),
		Description: text(doc.Selection, selectors.Description),
	}, nil
}

// overwriteNonEmpty сохраняет значение из списка, если детальная страница не
// дала поле либо её селектор вернул пустой текст.
func overwriteNonEmpty(vacancy *Vacancy, details Vacancy) {
	if details.Title != "" {
		vacancy.Title = details.Title
	}
	if details.Company != "" {
		vacancy.Company = details.Company
	}
	if details.Salary != "" {
		vacancy.Salary = details.Salary
	}
	if details.Description != "" {
		vacancy.Description = details.Description
	}
}

// loadAll выполняет ограниченный цикл динамической подгрузки и возвращает
// число реально выполненных итераций. Контекст позволяет прервать цикл извне.
func loadAll(ctx context.Context, source config.Source) (int, error) {
	// Отсутствующий блок pagination означает, что первая отрисовка уже полная.
	if source.Pagination == nil {
		return 0, nil
	}

	// Счётчик сбрасывается только при фактическом росте количества карточек.
	// Так один медленный ответ не завершает сбор, но цикл не становится вечным.
	noNewDataAttempts := 0
	for iteration := 1; iteration <= source.Pagination.MaxIterations; iteration++ {
		// Сравниваем количество до и после действия, поскольку сетевые события
		// у разных сайтов ненадёжны как универсальный признак готовности DOM.
		before, err := count(ctx, source.Selectors.Card)
		if err != nil {
			return iteration - 1, fmt.Errorf("count cards before pagination: %w", err)
		}
		if err := paginate(ctx, source.Pagination.ActionSelector); err != nil {
			return iteration - 1, fmt.Errorf("run pagination: %w", err)
		}
		if err := wait(ctx, source.Page.SettleDelay()); err != nil {
			return iteration - 1, err
		}
		// Явный стоп-селектор приоритетнее сравнения счётчиков, например когда
		// кнопка уже стала disabled, но в DOM ещё остались старые карточки.
		if stopped, err := shouldStop(ctx, source.Pagination.StopConditions); err != nil {
			return iteration - 1, err
		} else if stopped {
			return iteration, nil
		}
		after, err := count(ctx, source.Selectors.Card)
		if err != nil {
			return iteration - 1, fmt.Errorf("count cards after pagination: %w", err)
		}
		if after > before {
			noNewDataAttempts = 0
			continue
		}
		noNewDataAttempts++
		if noNewDataAttempts >= source.Pagination.MaxAttemptsWithoutNewData {
			return iteration, nil
		}
	}
	return source.Pagination.MaxIterations, nil
}

// count возвращает текущее число DOM-элементов по CSS-селектору.
func count(ctx context.Context, selector string) (int, error) {
	// strconv.Quote безопасно экранирует CSS-селектор при встраивании в JS.
	var result int
	if err := chromedp.Run(ctx, chromedp.Evaluate("document.querySelectorAll("+strconv.Quote(selector)+").length", &result)); err != nil {
		return 0, err
	}
	return result, nil
}

// paginate выполняет одну попытку подгрузки: скроллит страницу и при наличии
// селектора нажимает доступную кнопку «показать ещё».
func paginate(ctx context.Context, actionSelector string) error {
	// Скролл выполняется всегда: некоторые сайты подгружают данные по достижении
	// низа страницы. При наличии кнопки выполняется и клик по ней.
	// IIFE создаёт локальную область видимости и не оставляет const button в
	// глобальном контексте страницы между повторными Evaluate.
	expression := `(() => { window.scrollTo(0, document.body.scrollHeight);`
	if actionSelector != "" {
		expression += `const button = document.querySelector(` + strconv.Quote(actionSelector) + `); if (button && !button.disabled) button.click();`
	}
	expression += ` })()`
	return chromedp.Run(ctx, chromedp.Evaluate(expression, nil))
}

// shouldStop ищет явные признаки окончания выдачи, заданные в YAML.
func shouldStop(ctx context.Context, conditions config.StopConditions) (bool, error) {
	// Пустые селекторы допустимы: в таком случае завершение определяется только
	// отсутствием новых карточек и лимитом итераций.
	for _, selector := range []string{conditions.NoMoreResultsSelector, conditions.ButtonDisabledSelector} {
		if selector == "" {
			continue
		}
		var found bool
		if err := chromedp.Run(ctx, chromedp.Evaluate("document.querySelector("+strconv.Quote(selector)+") !== null", &found)); err != nil {
			return false, fmt.Errorf("check stop selector %q: %w", selector, err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// wait ждёт указанную длительность, но сразу возвращается при отмене контекста.
func wait(ctx context.Context, duration time.Duration) error {
	// time.Sleep не реагирует на отмену запуска. Таймер с select позволяет
	// немедленно завершить ожидание при общем run-timeout или отмене пользователя.
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parseHTML извлекает вакансии из ранее полученного HTML-контейнера и приводит
// относительные ссылки к абсолютным. Карточки без корректного href пропускаются.
func parseHTML(html string, source config.Source) ([]Vacancy, error) {
	// Разбор ведётся на статической копии уже отрисованного браузером HTML.
	// Благодаря этому извлечение не вызывает дополнительных действий в Chromium.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse rendered HTML: %w", err)
	}
	baseURL, err := url.Parse(source.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}

	var vacancies []Vacancy
	doc.Find(source.Selectors.Card).Each(func(_ int, card *goquery.Selection) {
		// Карточка без ссылки не годится для базового результата: пользователь не
		// сможет открыть вакансию, а будущая дедупликация не получит стабильный ключ.
		link, ok := card.Find(source.Selectors.Link).First().Attr("href")
		if !ok || strings.TrimSpace(link) == "" {
			return
		}
		// ResolveReference превращает относительные href в абсолютные URL сайта.
		parsedLink, err := url.Parse(link)
		if err != nil {
			return
		}
		vacancies = append(vacancies, Vacancy{
			Title:       text(card, source.Selectors.Title),
			Company:     text(card, source.Selectors.Company),
			Salary:      text(card, source.Selectors.Salary),
			Link:        baseURL.ResolveReference(parsedLink).String(),
			Description: text(card, source.Selectors.Description),
		})
	})
	return vacancies, nil
}

// text извлекает первое совпадение внутри карточки и нормализует пробельные символы.
func text(card *goquery.Selection, selector string) string {
	// Необязательные поля допускают пустой селектор. Fields одновременно
	// нормализует пробелы, переносы строк и неразрывные пробельные символы.
	if selector == "" {
		return ""
	}
	return strings.Join(strings.Fields(card.Find(selector).First().Text()), " ")
}
