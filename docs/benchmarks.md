# Benchmarks

Recorded measurements from `go test -bench <name> -benchmem -run '^$' ./<pkg>/`.
Not asserted — these are plain `testing.B` benchmarks with no thresholds,
recorded here as a log, one row per measurement run.

| date | commit | benchmark | ns/op | B/op | allocs/op | machine note |
| --- | --- | --- | --- | --- | --- | --- |
| 2026-09-25 | 88388b1-dirty | BenchmarkSystemPromptAssembly (internal/cli) | 22309 | 144752 | 313 | go1.26.3 darwin/arm64; Darwin Andres-MacBook-Pro.local 27.0.0 arm64 |
| 2026-09-25 | 88388b1-dirty | BenchmarkToolIndexRendering (internal/mcp) | 15778 | 72459 | 497 | go1.26.3 darwin/arm64; Darwin Andres-MacBook-Pro.local 27.0.0 arm64 |
| 2026-09-25 | 88388b1-dirty | BenchmarkCompaction (internal/compaction) | 68278 | 110370 | 1343 | go1.26.3 darwin/arm64; Darwin Andres-MacBook-Pro.local 27.0.0 arm64 |
