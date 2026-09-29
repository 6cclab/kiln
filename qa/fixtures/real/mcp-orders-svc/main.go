// Command orders-svc is a tiny HTTP service exposing an order list, used as
// a real-user fixture project for kiln QA scenarios: it ships with a
// pagination bug (see handlers.go) that an on-call incident points at.
package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/orders", listOrders)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
