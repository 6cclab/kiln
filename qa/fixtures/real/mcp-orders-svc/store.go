package main

// Order is one row in the in-memory order store.
type Order struct {
	ID       int     `json:"id"`
	Customer string  `json:"customer"`
	Total    float64 `json:"total"`
	Status   string  `json:"status"`
}

// orders is the seeded, in-memory order store. 17 orders, so that
// pagination with the default per_page of 10 leaves a partial last page
// (7 orders) and any page beyond that is out of range.
var orders = []Order{
	{1, "Aria Chen", 42.50, "shipped"},
	{2, "Marcus Diallo", 118.00, "shipped"},
	{3, "Priya Nair", 27.99, "delivered"},
	{4, "Tomas Novak", 64.20, "shipped"},
	{5, "Fatima Al-Sayed", 205.75, "delivered"},
	{6, "Liam O'Brien", 15.00, "cancelled"},
	{7, "Yuki Tanaka", 89.40, "shipped"},
	{8, "Elena Petrova", 33.10, "delivered"},
	{9, "Noah Kim", 156.60, "shipped"},
	{10, "Sofia Rossi", 72.25, "delivered"},
	{11, "Ahmed Hassan", 48.00, "shipped"},
	{12, "Grace Mensah", 91.99, "delivered"},
	{13, "Lucas Silva", 22.50, "shipped"},
	{14, "Mei Lin", 133.30, "cancelled"},
	{15, "Omar Farouk", 61.75, "delivered"},
	{16, "Ingrid Larsen", 44.00, "shipped"},
	{17, "Ravi Patel", 99.99, "shipped"},
}
