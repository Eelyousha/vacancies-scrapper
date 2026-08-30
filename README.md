# Vacancies scrapper

Минимальный CLI-парсер читает YAML-конфигурацию, загружает страницу через Chromium, выполняет пагинацию и выводит вакансии в JSON. Он пока не создаёт базу данных и не предоставляет UI: это проверочный этап для селекторов источника.

## Запуск

Требуется установленный Google Chrome или Chromium, доступный в `PATH`.

```sh
go run ./cmd/vacancy-scrape -config yandex_vacancies.yaml -max-results 10
```

Результат выводится в stdout. Для визуальной отладки браузера:

```sh
go run ./cmd/vacancy-scrape -config yandex_vacancies.yaml -headless=false
```

`page.timeout_seconds` ограничивает загрузку страницы. Общий лимит запуска задаётся отдельно: `-run-timeout=5m`.
Для быстрого пробного запуска, не меняя YAML, можно ограничить число попыток подгрузки: `-max-pagination-iterations=2`.
Если включён `detail_page`, ограничьте число открываемых вакансий: `-max-detail-pages=3`.

## Ограничения текущего этапа

* Не выполняется `search_on_ui` и не подставляется `search_url_template` — это будет добавлено вместе с параметром поискового запроса.
* Не создаются хеши, история, SQLite-записи и архивирование.
* Конфиг должен содержать `site_name`, абсолютный `base_url`, `page`, `selectors.container`, `selectors.card`, `selectors.title` и `selectors.link`.

При наличии `detail_page` CLI последовательно открывает ссылку каждой найденной вакансии и дополняет её полями с детальной страницы. Ошибки отдельных ссылок попадут в `detail_errors`, но не отменят выдачу списка.
