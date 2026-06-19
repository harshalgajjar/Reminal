package client

import (
	"embed"
	"fmt"
	"log"
	"net/http"

	"github.com/reminal/reminal/internal/config"
	"github.com/reminal/reminal/internal/relay"
)

//go:embed web/index.html
var webIndex embed.FS

func RunRelay(port string) error {
	if port == "" {
		port = config.DefaultPort
	}

	srv := relay.NewServer()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ws/{session}/{role}", func(w http.ResponseWriter, r *http.Request) {
		srv.HandleSessionWS(w, r, r.PathValue("session"), r.PathValue("role"))
	})
	mux.HandleFunc("/ws", srv.HandleWS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := webIndex.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "web UI unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	addr := ":" + port
	base := fmt.Sprintf("http://localhost%s", addr)
	fmt.Printf("reminal relay listening on %s\n", base)
	fmt.Printf("Local mode:  REMINAL_LOCAL=1 reminal\n")
	fmt.Printf("WebSocket:   ws://localhost%s/ws/<session>/<agent|viewer>\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
	return nil
}
