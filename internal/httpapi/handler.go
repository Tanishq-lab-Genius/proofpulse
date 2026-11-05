package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"proofpulse/internal/proofpulse"
	"proofpulse/web"
)

const maxRequestBody = 16 << 10

type Handler struct {
	service *proofpulse.Service
}

func NewHandler(service *proofpulse.Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.home)
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /api/verify", h.verify)
	return mux
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(web.Index)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "proofpulse"})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var request proofpulse.VerifyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Request body must contain one JSON object")
		return
	}
	request.Question = strings.TrimSpace(request.Question)
	request.Answer = strings.TrimSpace(request.Answer)
	if len(request.Question) < 3 || len(request.Question) > 2000 {
		writeError(w, http.StatusBadRequest, "question must be between 3 and 2000 characters")
		return
	}
	if len(request.Answer) > 12000 {
		writeError(w, http.StatusBadRequest, "answer must be at most 12000 characters")
		return
	}

	response, err := h.service.Verify(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"detail": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
