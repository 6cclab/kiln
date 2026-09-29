package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListOrders_FirstPage covers the common case: a full first page.
func TestListOrders_FirstPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/orders?page=1&per_page=10", nil)
	w := httptest.NewRecorder()
	listOrders(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got ordersResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Orders) != 10 {
		t.Fatalf("len(orders) = %d, want 10", len(got.Orders))
	}
	if got.Total != 17 {
		t.Fatalf("total = %d, want 17", got.Total)
	}
}

// TestListOrders_Defaults covers page/per_page omitted, which should fall
// back to page 1 and per_page 10.
func TestListOrders_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	w := httptest.NewRecorder()
	listOrders(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got ordersResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Page != 1 || got.PerPage != 10 {
		t.Fatalf("page/per_page = %d/%d, want 1/10", got.Page, got.PerPage)
	}
}

// Note: this suite does not cover page=3&per_page=10 (past the end of the
// 17-row store), which is exactly the case that panics in production. That
// gap is the point: the regression test a QA scenario adds should close it.
