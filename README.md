# Vacancies scrapper

Локальное Go-приложение собирает вакансии с сайтов работодателей, хранит их в
SQLite, отслеживает изменения и безопасно архивирует исчезнувшие объявления.
Источник добавляется через YAML только после browser dry run; локальный HTTP API
готов для будущего интерфейса.

## Быстрый старт

Требуется Google Chrome или Chromium в `PATH`.

```sh
go run ./cmd/vacancy-server -database vacancies.db
```

Сервер слушает `http://127.0.0.1:8080`; адрес меняется флагом `-address`.
Новая конфигурация проходит сценарий:

1. `POST /api/sources/test-config` с JSON `{ "yaml": "..." }` возвращает
   preview и одноразовый `test_token` на 15 минут.
2. `POST /api/sources` сохраняет YAML только с подходящим token.
3. `POST /api/sources/:id/run` выполняет сохранённый источник и записывает
   вакансии, историю, дельту и архивирование.

Контракт всех маршрутов описан в [_docs/04-api-ui.md](_docs/04-api-ui.md).
Пример YAML — в [_docs/examples/source.example.yaml](_docs/examples/source.example.yaml).

## Диагностический CLI

`vacancy-scraper` не сохраняет данные: он нужен для проверки селекторов и
печатает результат в JSON.

```sh
go run ./cmd/vacancy-scraper -config yandex_vacancies.yaml -max-results 10
```

Для визуальной отладки добавьте `-headless=false`. Общий лимит задаёт
`-run-timeout`; для короткой проверки доступны `-max-pagination-iterations` и
`-max-detail-pages`.

## Готово и далее

Готовы SQLite-миграции, идентификация по URL или fallback-фингерпринту,
история изменений, безопасная архивация, dry run, управление источниками и
backend API вакансий/запусков. Следующий крупный этап — локальный HTML/HTMX
интерфейс; расписания и фоновые запуски следуют после него.

Проверки проекта:

```sh
go test ./...
go vet ./...
go build ./...
```
