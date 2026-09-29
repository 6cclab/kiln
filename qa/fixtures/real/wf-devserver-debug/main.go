// Command webapp is a tiny HTTP API with one deliberate bug: /health
// reports the wrong content type, which the integration test in
// integration_test.go catches — but only against a running server, so
// fixing it requires starting the dev server and hitting it.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// Bug: body is JSON but the header still says text/plain.
	w.Header().Set("Content-Type", "text/plain")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8089"
	}
	http.HandleFunc("/health", healthHandler)
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
