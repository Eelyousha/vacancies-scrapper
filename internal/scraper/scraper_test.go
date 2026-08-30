package scraper

import (
	"os"
	"path/filepath"
	"testing"

	"vacancies-scrapper/internal/config"
)

// TestParseYandexListFixture проверяет контракт базового разбора на сохранённом
// HTML. Фикстура позволяет тестировать изменение кода без реального браузера.
func TestParseYandexListFixture(t *testing.T) {
	t.Parallel()
	html := readFixture(t, "yandex-list.html")
	source := config.Source{
		BaseURL: "https://yandex.ru/jobs/vacancies",
		Selectors: config.Selectors{
			Card:        "article[class^='VacancySnippet_wrapper__']",
			Title:       "h2[class*='VacancySnippet_title__']",
			Company:     "span[class^='VacancySnippetHeader_headerTitle__']",
			Link:        "a[class*='VacancySnippet_titleLink__']",
			Description: "div[class^='VacancySnippet_body__'] > p",
		},
	}

	vacancies, err := parseHTML(html, source)
	if err != nil {
		t.Fatalf("parseHTML() error = %v", err)
	}
	if len(vacancies) != 2 {
		t.Fatalf("parseHTML() returned %d vacancies, want 2", len(vacancies))
	}

	first := vacancies[0]
	if first.Title != "Go-разработчик" {
		t.Errorf("first title = %q", first.Title)
	}
	if first.Company != "Яндекс 360" {
		t.Errorf("first company = %q", first.Company)
	}
	if first.Link != "https://yandex.ru/jobs/vacancies/backend-go-101" {
		t.Errorf("first link = %q", first.Link)
	}
	if first.Description != "Разработка высоконагруженных сервисов." {
		t.Errorf("first description = %q", first.Description)
	}
	if vacancies[1].Link != "https://yandex.ru/jobs/vacancies/data-analyst-202" {
		t.Errorf("absolute link = %q", vacancies[1].Link)
	}
}

// TestParseDetailFixture проверяет извлечение полей, которые не помещаются
// в карточку списка и приходят только с отдельной страницы вакансии.
func TestParseDetailFixture(t *testing.T) {
	t.Parallel()
	html := readFixture(t, "yandex-detail.html")
	details, err := parseDetailHTML(html, config.DetailSelectors{
		Title:       "h1[class^='VacancyPage_title__']",
		Company:     "div[class^='VacancyPage_company__']",
		Salary:      "div[class^='VacancyPage_salary__']",
		Description: "div[class^='VacancyPage_description__']",
	})
	if err != nil {
		t.Fatalf("parseDetailHTML() error = %v", err)
	}
	if details.Title != "Go-разработчик в инфраструктурную команду" {
		t.Errorf("title = %q", details.Title)
	}
	if details.Salary != "от 250 000 ₽" {
		t.Errorf("salary = %q", details.Salary)
	}
	if details.Description != "Проектирование сервисов. Поддержка и развитие платформы." {
		t.Errorf("description = %q", details.Description)
	}
}

// TestOverwriteNonEmpty гарантирует, что неполные данные детальной страницы
// не стирают полезные значения, уже извлечённые из карточки списка.
func TestOverwriteNonEmpty(t *testing.T) {
	t.Parallel()
	vacancy := Vacancy{Title: "Название из списка", Company: "Компания из списка", Description: "Короткое описание"}
	overwriteNonEmpty(&vacancy, Vacancy{Title: "Название из детали", Salary: "100 000 ₽"})

	if vacancy.Title != "Название из детали" || vacancy.Company != "Компания из списка" || vacancy.Salary != "100 000 ₽" || vacancy.Description != "Короткое описание" {
		t.Errorf("unexpected merged vacancy: %#v", vacancy)
	}
}

// readFixture возвращает HTML рядом с тестом и завершает тест понятной ошибкой,
// если фикстура случайно удалена или переименована.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return string(data)
}
