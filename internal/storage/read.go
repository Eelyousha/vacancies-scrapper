package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Vacancy struct{ ID, SourceID, Title, Company, Salary, Link, CanonicalLink, Description, ListingStatus, UserStatus, LastRunID string }
type VacancyFilter struct {
	SourceID, ListingStatus, UserStatus, RunID, Search string
	Sort, Direction                                    string
	Limit, Offset                                      int
}

func (s *Store) ListVacancies(ctx context.Context, filter VacancyFilter) ([]Vacancy, int, error) {
	where := []string{"1=1"}
	args := []any{}
	add := func(expr, value string) {
		if value != "" {
			where = append(where, expr)
			args = append(args, value)
		}
	}
	add("v.source_id = ?", filter.SourceID)
	add("v.listing_status = ?", filter.ListingStatus)
	add("v.user_status = ?", filter.UserStatus)
	if filter.RunID != "" {
		where = append(where, "EXISTS (SELECT 1 FROM scraping_run_vacancies srv WHERE srv.run_id = ? AND srv.vacancy_id = v.id)")
		args = append(args, filter.RunID)
	}
	if filter.Search != "" {
		where = append(where, "(LOWER(v.title) LIKE LOWER(?) OR LOWER(v.company) LIKE LOWER(?))")
		q := "%" + filter.Search + "%"
		args = append(args, q, q)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM vacancies v WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count vacancies: %w", err)
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	sortColumn := map[string]string{"title": "v.title", "company": "v.company", "created_at": "v.created_at", "updated_at": "v.updated_at"}[filter.Sort]
	if sortColumn == "" {
		sortColumn = "v.updated_at"
	}
	direction := "DESC"
	if strings.EqualFold(filter.Direction, "asc") {
		direction = "ASC"
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT v.id,v.source_id,v.title,v.company,COALESCE(v.salary,''),v.link,v.canonical_link,COALESCE(v.description,''),v.listing_status,v.user_status,COALESCE(v.last_run_id,'') FROM vacancies v WHERE "+clause+" ORDER BY "+sortColumn+" "+direction+" LIMIT ? OFFSET ?", append(args, filter.Limit, filter.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list vacancies: %w", err)
	}
	defer rows.Close()
	var items []Vacancy
	for rows.Next() {
		var v Vacancy
		if err := rows.Scan(&v.ID, &v.SourceID, &v.Title, &v.Company, &v.Salary, &v.Link, &v.CanonicalLink, &v.Description, &v.ListingStatus, &v.UserStatus, &v.LastRunID); err != nil {
			return nil, 0, err
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}
func (s *Store) UpdateVacancyUserStatus(ctx context.Context, id, status string) (Vacancy, error) {
	if status != "new" && status != "viewed" && status != "hidden" {
		return Vacancy{}, fmt.Errorf("unsupported user status %q", status)
	}
	r, err := s.DB.ExecContext(ctx, "UPDATE vacancies SET user_status=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", status, id)
	if err != nil {
		return Vacancy{}, err
	}
	if err := requireAffectedRow(r); err != nil {
		return Vacancy{}, fmt.Errorf("vacancy %q: %w", id, err)
	}
	return s.getVacancy(ctx, id)
}
func (s *Store) getVacancy(ctx context.Context, id string) (Vacancy, error) {
	var v Vacancy
	err := s.DB.QueryRowContext(ctx, "SELECT id,source_id,title,company,COALESCE(salary,''),link,canonical_link,COALESCE(description,''),listing_status,user_status,COALESCE(last_run_id,'') FROM vacancies WHERE id=?", id).Scan(&v.ID, &v.SourceID, &v.Title, &v.Company, &v.Salary, &v.Link, &v.CanonicalLink, &v.Description, &v.ListingStatus, &v.UserStatus, &v.LastRunID)
	if err == sql.ErrNoRows {
		return Vacancy{}, fmt.Errorf("vacancy %q: %w", id, ErrNotFound)
	}
	return v, err
}
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.DB.QueryContext(ctx, sourceSelect+" ORDER BY name, slug")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Source
	for rows.Next() {
		source, err := scanSourceRow(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, source)
	}
	return values, rows.Err()
}
func scanSourceRow(rows *sql.Rows) (Source, error) {
	var source Source
	var active int
	var last, created, updated string
	if err := rows.Scan(&source.ID, &source.Slug, &source.Name, &source.ConfigYAML, &active, &source.ScheduleType, &source.ScheduleValue, &source.ScheduleTimezone, &last, &source.LastError, &source.Status, &created, &updated); err != nil {
		return Source{}, err
	}
	var err error
	source.LastRunAt, err = parseOptionalTime(last)
	if err != nil {
		return Source{}, err
	}
	source.CreatedAt, err = parseTime(created)
	if err != nil {
		return Source{}, err
	}
	source.UpdatedAt, err = parseTime(updated)
	source.IsActive = active != 0
	return source, err
}
func (s *Store) LatestRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.DB.QueryContext(ctx, runSelect+" ORDER BY started_at DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Run
	for rows.Next() {
		var r Run
		var started, finished string
		if err := rows.Scan(&r.ID, &r.SourceID, &started, &finished, &r.Status, &r.CompletionStatus, &r.AddedCount, &r.UpdatedCount, &r.ArchivedCount, &r.SeenCount, &r.ErrorMessage, &r.MetaJSON); err != nil {
			return nil, err
		}
		var e error
		r.StartedAt, e = parseTime(started)
		if e != nil {
			return nil, e
		}
		r.FinishedAt, e = parseOptionalTime(finished)
		if e != nil {
			return nil, e
		}
		values = append(values, r)
	}
	return values, rows.Err()
}
