# webapp

`go run .` starts an HTTP server (default :8089) with one `/health` route.
`integration_test.go` requires that server to be running (it skips, not
fails, if nothing answers on the port) and checks that `/health` reports
`Content-Type: application/json` — it currently reports `text/plain`, a
one-line bug in `healthHandler` in `main.go`.
