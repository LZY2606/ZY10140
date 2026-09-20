// Command sonde-server runs the local sounding assembly application:
// SQLite storage, JSON API and the browser profile UI.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"sonde/internal/domain"
	"sonde/internal/server"
	"sonde/internal/service"
	"sonde/internal/sim"
	"sonde/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5340", "listen address")
	dbPath := flag.String("db", "sonde.db", "sqlite database path")
	seed := flag.Bool("seed", true, "auto-create a demo sounding when the database is empty")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()
	svc := service.New(st)

	if *seed {
		ctx := context.Background()
		soundings, err := svc.ListSoundings(ctx)
		if err != nil {
			log.Fatalf("list: %v", err)
		}
		if len(soundings) == 0 {
			sd, err := svc.CreateSounding(ctx, sim.DefaultConfig().DeviceID, "演示探空（含乱序/冲突/结冰/GPS断点）")
			if err != nil {
				log.Fatalf("seed sounding: %v", err)
			}
			dels := sim.Generate(sim.DefaultConfig())
			const batch = 25
			for i := 0; i < len(dels); i += batch {
				end := i + batch
				if end > len(dels) {
					end = len(dels)
				}
				pkts := make([]domain.Packet, 0, end-i)
				for _, d := range dels[i:end] {
					p := d.Packet
					p.ReceivedAt = d.ReceivedAt
					pkts = append(pkts, p)
				}
				if _, _, err := svc.Ingest(ctx, sd.ID, pkts); err != nil {
					log.Fatalf("seed ingest: %v", err)
				}
			}
			log.Printf("已创建演示探空 #%d（%d 个投递包）", sd.ID, len(dels))
		}
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(svc).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("探空组装台监听 http://%s", *listen)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
