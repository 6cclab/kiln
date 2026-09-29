# orders-svc conventions

- Stdlib only, no third-party dependencies.
- Handlers live in `handlers.go`; the seeded in-memory store lives in
  `store.go`. Keep them separate.
- Every handler change needs a matching case in `handlers_test.go`,
  including edge cases (empty results, out-of-range pages).
- Run `go vet ./...` and `go test ./...` before considering a change done.
