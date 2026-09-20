package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var webFS embed.FS

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		http.ServeFileFS(w, r, webFS, "web/index.html")
		return
	}
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	http.FileServer(http.FS(sub)).ServeHTTP(w, r)
}
