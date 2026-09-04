# Модель данных

Все даты — UTC в ISO 8601. При открытии SQLite-соединения включается `PRAGMA foreign_keys = ON`. Изменение схемы выполняется миграциями.

## `sources`

| Поле | Назначение |
|---|---|
| `id` | UUID, первичный ключ |
| `slug` | Уникальное читаемое имя |
| `name` | Название компании/источника |
| `config_yaml` | YAML, успешно прошедший dry run |
| `is_active` | Разрешены ли автоматические запуски |
| `schedule_type` | `manual`, `cron`, `interval` |
| `schedule_value` | cron-выражение или интервал, например `2h` |
| `schedule_timezone` | IANA-таймзона для cron-расписания |
| `last_run_at`, `last_error` | Последний запуск и ошибка |
| `status` | `idle`, `running`, `success`, `failed`, `partial` |
| `created_at`, `updated_at` | Аудит записи |

Интервал рассчитывается от завершения последнего успешного запуска и не меняется при смене системной зоны. Для cron сохраняется IANA-таймзона; при смене системной зоны интерфейс предлагает сохранить старую либо принять новую. `is_active` не запрещает ручной запуск.

## `vacancies`

| Поле | Назначение |
|---|---|
| `id` | SHA-256 от `source_id` и нормализованного URL |
| `source_id` | FK на `sources` |
| `title`, `company`, `salary` | Извлечённые поля |
| `link`, `canonical_link` | Исходная и нормализованная ссылка |
| `description` | Сохраняется для будущей сверки, пока не историзируется |
| `listing_status` | Состояние на сайте: `active`, `archived` |
| `user_status` | Состояние пользователя: `new`, `viewed`, `hidden` |
| `last_run_id` | FK на последний запуск, увидевший вакансию |
| `created_at`, `updated_at`, `archived_at` | Временные отметки |

Внешние ID разных платформ не нужны и отдельных таблиц под них не требуется.
Нормализация разрешает относительные URL, удаляет фрагмент, приводит схему/хост
и удаляет согласованные tracking-параметры. Изменение канонического URL всегда
создаёт новую вакансию; исходный `link` при неизменном каноническом URL не
историзируется. Если сайт генерирует новую ссылку на одну и ту же вакансию при
каждом запуске, YAML должен задавать fallback-фингерпринт; изменение его состава
также считается новой вакансией.

## История и запуски

`vacancy_history` содержит `id`, `vacancy_id`, `field_name`, `old_value`, `new_value`, `changed_at`. В первом релизе отслеживаются `title`, `company`, `salary`; `description` только сохраняется. Канонический URL является идентичностью, поэтому его смена создаёт новую вакансию, а не запись истории.

`scraping_runs` содержит `id`, `source_id`, `started_at`, `finished_at`, `status` (`running`, `success`, `failed`, `partial`), `completion_status` (`complete`, `incomplete`, `unknown`), счётчики `added_count`, `updated_count`, `archived_count`, `seen_count`, а также `error_message` и `meta_json` для диагностик.

`scraping_run_vacancies` связывает `run_id` и `vacancy_id`, с `disposition`: `added`, `updated`, `unchanged`, `archived`. Она позволяет показать полный результат определённого запуска, а не только вакансии, изменённые в нём.

Индексы: `vacancies(source_id, listing_status)`, `vacancies(last_run_id)`, `scraping_runs(source_id, started_at DESC)`, `scraping_run_vacancies(run_id)` и индексы внешних ключей.
