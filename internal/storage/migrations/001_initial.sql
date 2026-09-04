CREATE TABLE sources (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    config_yaml TEXT NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    schedule_type TEXT NOT NULL DEFAULT 'manual' CHECK (schedule_type IN ('manual', 'cron', 'interval')),
    schedule_value TEXT,
    schedule_timezone TEXT,
    last_run_at TEXT,
    last_error TEXT,
    status TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'running', 'success', 'failed', 'partial')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE scraping_runs (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    started_at TEXT NOT NULL,
    finished_at TEXT,
    status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed', 'partial')),
    completion_status TEXT NOT NULL DEFAULT 'unknown' CHECK (completion_status IN ('complete', 'incomplete', 'unknown')),
    added_count INTEGER NOT NULL DEFAULT 0,
    updated_count INTEGER NOT NULL DEFAULT 0,
    archived_count INTEGER NOT NULL DEFAULT 0,
    seen_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    meta_json TEXT
);

CREATE INDEX scraping_runs_source_started_idx ON scraping_runs(source_id, started_at DESC);

CREATE TABLE vacancies (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    title TEXT NOT NULL,
    company TEXT NOT NULL,
    salary TEXT,
    link TEXT NOT NULL,
    canonical_link TEXT NOT NULL,
    description TEXT,
    listing_status TEXT NOT NULL DEFAULT 'active' CHECK (listing_status IN ('active', 'archived')),
    user_status TEXT NOT NULL DEFAULT 'new' CHECK (user_status IN ('new', 'viewed', 'hidden')),
    last_run_id TEXT REFERENCES scraping_runs(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT,
    UNIQUE (source_id, canonical_link)
);

CREATE INDEX vacancies_source_listing_idx ON vacancies(source_id, listing_status);
CREATE INDEX vacancies_last_run_idx ON vacancies(last_run_id);

CREATE TABLE vacancy_history (
    id TEXT PRIMARY KEY,
    vacancy_id TEXT NOT NULL REFERENCES vacancies(id),
    field_name TEXT NOT NULL CHECK (field_name IN ('title', 'company', 'salary', 'link')),
    old_value TEXT,
    new_value TEXT,
    changed_at TEXT NOT NULL
);

CREATE INDEX vacancy_history_vacancy_idx ON vacancy_history(vacancy_id);

CREATE TABLE scraping_run_vacancies (
    run_id TEXT NOT NULL REFERENCES scraping_runs(id),
    vacancy_id TEXT NOT NULL REFERENCES vacancies(id),
    disposition TEXT NOT NULL CHECK (disposition IN ('added', 'updated', 'unchanged', 'archived')),
    PRIMARY KEY (run_id, vacancy_id)
);

CREATE INDEX scraping_run_vacancies_run_idx ON scraping_run_vacancies(run_id);
