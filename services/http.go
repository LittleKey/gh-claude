// Package services provides independent services for gh-claude
package services

import (
	"encoding/json"
	"log"
	"net/http"
)

// HttpServer handles HTTP requests
type HttpServer struct {
	mux *http.ServeMux
}

// NewHttpServer creates a new HttpServer
func NewHttpServer() *HttpServer {
	return &HttpServer{
		mux: http.NewServeMux(),
	}
}

// Handle registers a handler for a path
func (h *HttpServer) Handle(path string, handler http.HandlerFunc) {
	h.mux.HandleFunc(path, handler)
}

// HandleFunc registers a handler function for a path
func (h *HttpServer) HandleFunc(path string, handler func(http.ResponseWriter, *http.Request)) {
	h.mux.HandleFunc(path, handler)
}

// ServeHTTP implements http.Handler
func (h *HttpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// WriteJSON writes JSON response
func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[HttpServer] Failed to encode JSON: %v", err)
	}
}

// WriteError writes error response
func WriteError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, map[string]interface{}{
		"success": false,
		"error":   err.Error(),
	})
}

// WriteSuccess writes success response
func WriteSuccess(w http.ResponseWriter, data interface{}) {
	WriteJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"data":    data,
	})
}

// ParseRequest parses a JSON request body
func ParseRequest(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// GetQueryParam returns a query parameter
func GetQueryParam(r *http.Request, name string) string {
	return r.URL.Query().Get(name)
}
