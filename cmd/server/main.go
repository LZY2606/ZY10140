// Command server runs the local sounding assembly application: Go API,
// embedded browser UI, SQLite storage and a controllable packet simulator.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"soundingapp/internal/api"
	"soundingapp/internal/service"
	"soundingapp/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5340", "listen address")
	dbPath := flag.String("db", "sounding.db", "SQLite database path")
	flag.Parse()

	if v := os.Getenv("SOUNDING_DB"); v != "" {
		*dbPath = v
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()

	svc := service.New(st)
	svc.Now = func() time.Time { return time.Now().UTC() }

	sim := api.NewSimServer(svc, "demo")
	srv := api.New(svc, sim)

	log.Printf("sounding app listening on http://%s (db=%s)", *listen, *dbPath)
	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := hs.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
