package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Source — полное декларативное описание одного сайта-источника. Экземпляр
// создаётся из YAML и передаётся скраперу без привязки к БД или HTTP-слою.
type Source struct {
	// SiteName отображается в результате запуска и позднее будет сохранён у источника.
	SiteName string `yaml:"site_name"`
	// BaseURL — стартовая абсолютная страница со списком вакансий.
	BaseURL string `yaml:"base_url"`
	// SearchURLTemplate описывает поиск через URL; на текущем этапе не исполняется.
	SearchURLTemplate string `yaml:"search_url_template"`
	// SearchOnUI описывает поиск через элементы страницы; пока резерв для следующего этапа.
	SearchOnUI *SearchOnUI `yaml:"search_on_ui"`
	// Page задаёт ожидание первоначальной отрисовки и паузу после подгрузки.
	Page Page `yaml:"page"`
	// Pagination отсутствует, когда все карточки уже есть в первом DOM.
	Pagination *Pagination `yaml:"pagination"`
	// Selectors содержит CSS-селекторы контейнера, карточки и её полей.
	Selectors Selectors `yaml:"selectors"`
	// DetailPage задаёт необязательное обогащение вакансии данными с её отдельной страницы.
	DetailPage *DetailPage `yaml:"detail_page"`
	// Identity хранит правила fallback-идентификации для будущего слоя БД.
	Identity *Identity `yaml:"identity"`
}

// SearchOnUI описывает действия, необходимые для ввода поискового запроса в UI.
type SearchOnUI struct {
	// InputSelector указывает поле поиска.
	InputSelector string `yaml:"input_selector"`
	// SubmitSelector указывает кнопку отправки формы, если она существует.
	SubmitSelector string `yaml:"submit_selector"`
	// Enter означает отправку клавишей Enter вместо нажатия кнопки.
	Enter bool `yaml:"enter"`
}

// Page содержит параметры первоначальной загрузки и ожидания динамического DOM.
type Page struct {
	// WaitForSelector должен появиться после отрисовки списка вакансий.
	WaitForSelector string `yaml:"wait_for_selector"`
	// TimeoutSeconds ограничивает ожидание WaitForSelector, а не весь запуск.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// SettleDelayMS — пауза после скролла или клика перед повторным подсчётом карточек.
	SettleDelayMS int `yaml:"settle_delay_ms"`
}

// Timeout переводит значение YAML в time.Duration для chromedp.Poll.
func (p Page) Timeout() time.Duration {
	return time.Duration(p.TimeoutSeconds) * time.Second
}

// SettleDelay переводит миллисекунды YAML в duration, удобный для отменяемого ожидания.
func (p Page) SettleDelay() time.Duration {
	return time.Duration(p.SettleDelayMS) * time.Millisecond
}

// Pagination описывает безопасную ограниченную догрузку карточек на странице.
// На текущем этапе поддерживается единственный режим scroll_or_button.
type Pagination struct {
	// Type определяет алгоритм пагинации.
	Type string `yaml:"type"`
	// ActionSelector — необязательная кнопка «показать ещё».
	ActionSelector string `yaml:"action_selector"`
	// MaxAttemptsWithoutNewData защищает от бесконечного цикла без новых карточек.
	MaxAttemptsWithoutNewData int `yaml:"max_attempts_without_new_data"`
	// MaxIterations ограничивает общее число действий даже на нестабильной странице.
	MaxIterations int `yaml:"max_iterations"`
	// StopConditions содержит явные признаки окончания выдачи.
	StopConditions StopConditions `yaml:"stop_conditions"`
}

// StopConditions объединяет необязательные CSS-селекторы завершения пагинации.
type StopConditions struct {
	// NoMoreResultsSelector указывает сообщение об отсутствии следующих результатов.
	NoMoreResultsSelector string `yaml:"no_more_results_selector"`
	// ButtonDisabledSelector указывает неактивную кнопку дальнейшей загрузки.
	ButtonDisabledSelector string `yaml:"button_disabled_selector"`
}

// Selectors содержит CSS-селекторы. Поля вакансии всегда ищутся внутри Card.
type Selectors struct {
	// Container охватывает весь список карточек в финальном DOM.
	Container string `yaml:"container"`
	// Card выбирает одну повторяющуюся карточку вакансии.
	Card string `yaml:"card"`
	// Title и Link обязательны для полезной вакансии.
	Title       string `yaml:"title"`
	Company     string `yaml:"company"`
	Salary      string `yaml:"salary"`
	Link        string `yaml:"link"`
	Description string `yaml:"description"`
}

// DetailPage описывает загрузку каждой вакансии по ссылке из карточки списка.
// Если блок отсутствует, скрапер работает только со списком вакансий.
type DetailPage struct {
	// WaitForSelector должен появиться на детальной странице до извлечения полей.
	WaitForSelector string `yaml:"wait_for_selector"`
	// TimeoutSeconds ограничивает ожидание готовности одной детальной страницы.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// Selectors ищутся в документе детальной страницы, а не внутри карточки списка.
	Selectors DetailSelectors `yaml:"selectors"`
}

// DetailSelectors содержит поля вакансии на её собственной странице. Любое
// непустое поле заменяет соответствующее значение, полученное из списка.
type DetailSelectors struct {
	Title       string `yaml:"title"`
	Company     string `yaml:"company"`
	Salary      string `yaml:"salary"`
	Description string `yaml:"description"`
}

// Identity хранит поля fallback-фингерпринта на случай отсутствия стабильной ссылки.
// Базовый CLI его ещё не применяет, но сохраняет контракт YAML для будущего хранилища.
type Identity struct {
	FallbackFields []string `yaml:"fallback_fields"`
}

// Load читает YAML-файл, запрещает неизвестные поля и возвращает уже
// проверенную конфигурацию. Ошибка никогда не возвращает частично валидный Source.
func Load(path string) (Source, error) {
	// KnownFields запрещает опечатки и устаревшие ключи: молчаливое игнорирование
	// поля в конфиге могло бы привести к неполному или неверному сбору.
	data, err := os.ReadFile(path)
	if err != nil {
		return Source{}, fmt.Errorf("read config %q: %w", path, err)
	}

	return Parse(data)
}

// Parse validates one YAML document supplied by an API request or a file.
func Parse(data []byte) (Source, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var source Source
	if err := decoder.Decode(&source); err != nil {
		return Source{}, fmt.Errorf("decode YAML: %w", err)
	}
	if err := source.Validate(); err != nil {
		return Source{}, err
	}
	return source, nil
}

// Validate проверяет обязательные поля и числовые ограничения, доступные без
// обращения к сайту. Семантическая проверка селекторов выполняется dry run-ом.
func (s Source) Validate() error {
	// Проверяются только инварианты, которые можно установить без браузера.
	// Корректность CSS-селекторов и наличие данных проверяются dry run-ом.
	if strings.TrimSpace(s.SiteName) == "" {
		return fmt.Errorf("site_name is required")
	}
	parsedURL, err := url.ParseRequestURI(s.BaseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("base_url must be an absolute URL")
	}
	if s.Page.TimeoutSeconds <= 0 {
		return fmt.Errorf("page.timeout_seconds must be greater than zero")
	}
	if s.Page.SettleDelayMS < 0 {
		return fmt.Errorf("page.settle_delay_ms cannot be negative")
	}
	if err := requiredSelectors(s.Selectors); err != nil {
		return err
	}
	if err := validateDetailPage(s.DetailPage); err != nil {
		return err
	}
	if err := validateIdentity(s.Identity); err != nil {
		return err
	}
	if s.Pagination == nil {
		return nil
	}
	if s.Pagination.Type != "scroll_or_button" {
		return fmt.Errorf("pagination.type must be scroll_or_button")
	}
	if s.Pagination.MaxAttemptsWithoutNewData <= 0 {
		return fmt.Errorf("pagination.max_attempts_without_new_data must be greater than zero")
	}
	if s.Pagination.MaxIterations <= 0 {
		return fmt.Errorf("pagination.max_iterations must be greater than zero")
	}
	return nil
}

func validateIdentity(identity *Identity) error {
	if identity == nil {
		return nil
	}
	if len(identity.FallbackFields) == 0 {
		return fmt.Errorf("identity.fallback_fields must contain at least one field")
	}
	allowed := map[string]struct{}{"title": {}, "company": {}, "salary": {}, "description": {}}
	seen := make(map[string]struct{}, len(identity.FallbackFields))
	for _, field := range identity.FallbackFields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("identity.fallback_fields contains unsupported field %q", field)
		}
		if _, duplicate := seen[field]; duplicate {
			return fmt.Errorf("identity.fallback_fields contains duplicate field %q", field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

// validateDetailPage проверяет необязательный блок обогащения детальными
// страницами. Он должен содержать хотя бы одно поле, иначе лишний обход сайта
// не приносит новых данных.
func validateDetailPage(detailPage *DetailPage) error {
	if detailPage == nil {
		return nil
	}
	if strings.TrimSpace(detailPage.WaitForSelector) == "" {
		return fmt.Errorf("detail_page.wait_for_selector is required")
	}
	if detailPage.TimeoutSeconds <= 0 {
		return fmt.Errorf("detail_page.timeout_seconds must be greater than zero")
	}
	selectors := detailPage.Selectors
	if selectors.Title == "" && selectors.Company == "" && selectors.Salary == "" && selectors.Description == "" {
		return fmt.Errorf("detail_page.selectors must contain at least one selector")
	}
	return nil
}

// requiredSelectors проверяет минимальный набор селекторов для извлечения
// пригодной к просмотру и последующей идентификации вакансии.
func requiredSelectors(selectors Selectors) error {
	// Без контейнера, карточки, названия и ссылки невозможно безопасно показать
	// результат или позднее сопоставить его с сохранённой вакансией.
	for name, value := range map[string]string{
		"selectors.container": selectors.Container,
		"selectors.card":      selectors.Card,
		"selectors.title":     selectors.Title,
		"selectors.link":      selectors.Link,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}
