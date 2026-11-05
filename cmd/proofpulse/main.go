package main

import (
	"log"
	"net/http"
	"time"

	"proofpulse/internal/config"
	"proofpulse/internal/httpapi"
	"proofpulse/internal/proofpulse"
)

func main() {
	cfg := config.Load()
	service := proofpulse.NewService(cfg)
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           httpapi.NewHandler(service),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("ProofPulse listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
