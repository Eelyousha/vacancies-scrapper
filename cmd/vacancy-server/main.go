package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"vacancies-scrapper/internal/dryrun"
	"vacancies-scrapper/internal/httpapi"
	"vacancies-scrapper/internal/scraper"
	"vacancies-scrapper/internal/storage"
)

func main() {
	address := flag.String("address", "127.0.0.1:8080", "local HTTP listen address")
	databasePath := flag.String("database", "vacancies.db", "path to SQLite database")
	runTimeout := flag.Duration("run-timeout", httpapi.DefaultManualRunTimeout, "maximum duration of one manual source run")
	logFilePath := flag.String("log-file", "", "append server logs to this file as well as stderr")
	flag.Parse()
	if *logFilePath != "" {
		logFile, err := os.OpenFile(*logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatal(fmt.Errorf("open log file: %w", err))
		}
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}
	store, err := storage.Open(context.Background(), *databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if err := store.RecoverInterruptedRuns(context.Background()); err != nil {
		log.Fatal(fmt.Errorf("recover interrupted runs: %w", err))
	}
	browser := scraper.New(true, 0)
	handler := httpapi.NewWithRunTimeout(store, dryrun.New(browser, 15*time.Minute), browser, *runTimeout)
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("vacancy server listening on http://%s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("serve HTTP API: %w", err))
	}
}
