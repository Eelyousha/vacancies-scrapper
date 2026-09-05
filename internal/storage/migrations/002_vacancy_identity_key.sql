CREATE TABLE vacancies_new (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES sources(id),
    title TEXT NOT NULL,
    company TEXT NOT NULL,
    salary TEXT,
    link TEXT NOT NULL,
    canonical_link TEXT NOT NULL,
    identity_key TEXT NOT NULL,
    description TEXT,
    listing_status TEXT NOT NULL DEFAULT 'active' CHECK (listing_status IN ('active', 'archived')),
    user_status TEXT NOT NULL DEFAULT 'new' CHECK (user_status IN ('new', 'viewed', 'hidden')),
    last_run_id TEXT REFERENCES scraping_runs(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT
);

INSERT INTO vacancies_new (
    id, source_id, title, company, salary, link, canonical_link, identity_key,
    description, listing_status, user_status, last_run_id, created_at, updated_at, archived_at
)
SELECT id, source_id, title, company, salary, link, canonical_link, canonical_link,
    description, listing_status, user_status, last_run_id, created_at, updated_at, archived_at
FROM vacancies;

DROP TABLE vacancies;
ALTER TABLE vacancies_new RENAME TO vacancies;

CREATE INDEX vacancies_source_listing_idx ON vacancies(source_id, listing_status);
CREATE INDEX vacancies_last_run_idx ON vacancies(last_run_id);
CREATE UNIQUE INDEX vacancies_source_identity_idx ON vacancies(source_id, identity_key);
