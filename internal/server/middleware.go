package server

import (
	"log"
	"net/http"
	"time"

	swaggerFiles "github.com/swaggo/files"
)

// swaggerHandler 提供 Swagger UI（静态资源内嵌于 swaggo/files，无外部 CDN 依赖）。
func (s *Server) swaggerHandler() http.Handler {
	h := swaggerFiles.NewHandler()
	h.Prefix = "/swagger/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/swagger" {
			http.Redirect(w, r, "/swagger/index.html?url=/openapi.yaml", http.StatusFound)
			return
		}
		if r.URL.Path == "/swagger/" {
			http.Redirect(w, r, "/swagger/index.html?url=/openapi.yaml", http.StatusFound)
			return
		}
		h.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
