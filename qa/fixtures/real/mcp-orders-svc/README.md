# orders-svc

A tiny HTTP service exposing a paginated order list:

```
GET /orders?page=<n>&per_page=<n>
```

Returns `{"page","per_page","total","orders"}` JSON over an in-memory,
17-row order store (see `store.go`).

## Running

```
go run .
```

Serves on `:8080`.

## Testing

```
go vet ./...
go test ./...
```
