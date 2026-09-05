package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/httpapi"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func main() {
	address := flag.String("address", "127.0.0.1:8080", "local HTTP listen address")
	databasePath := flag.String("database", "vacancies.db", "path to SQLite database")
	flag.Parse()
	store, err := storage.Open(context.Background(), *databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	browser := scraper.New(true, 0)
	handler := httpapi.New(store, dryrun.New(browser, 15*time.Minute), browser)
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("vacancy server listening on http://%s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("serve HTTP API: %w", err))
	}
}
