package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// ordersResponse is the JSON body returned by listOrders.
type ordersResponse struct {
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
	Total   int     `json:"total"`
	Orders  []Order `json:"orders"`
}

// listOrders handles GET /orders?page=&per_page=, returning one page of the
// order store.
//
// BUG: end is clamped to len(orders) but start is not, so a page past the
// end of the store (e.g. page=3&per_page=10 against 17 orders: start=20,
// end clamped to 17) slices orders[20:17], which panics with "runtime
// error: slice bounds out of range [20:17]" because start > end.
func listOrders(w http.ResponseWriter, r *http.Request) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	perPage, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil || perPage < 1 {
		perPage = 10
	}

	start := (page - 1) * perPage
	end := start + perPage
	if end > len(orders) {
		end = len(orders)
	}

	pageOrders := orders[start:end]

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ordersResponse{
		Page:    page,
		PerPage: perPage,
		Total:   len(orders),
		Orders:  pageOrders,
	})
}
