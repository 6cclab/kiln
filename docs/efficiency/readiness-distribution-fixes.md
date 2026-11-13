# Readiness distribution fixes

This change fixes basic install, setup and release issues found in a read-only audit of `5a7150f`. It does not implement the separate efficiency roadmap.

## What changed

- `scripts/install.sh` now preserves `kiln.exe` for Windows release and source installs, including the temporary file used for replacement. Unix installs keep `kiln`. A running Windows executable may be locked by the OS, so the install instructions say to close kiln before updating.
- `kiln doctor` now puts an unavailable or malformed selected model in the problem list while keeping its existing report-only exit status of 0. The tier line still shows the resolution error, and the problem names `kiln models` and `--model` as next steps.
- README and usage/troubleshooting docs now explain how to select an available local or authenticated model before the first run, update and uninstall, the five prebuilt targets, fullscreen as the default, and Stop hook continuation.
- Release publishing now waits for `make check`, `make e2e` (including installer fixtures), and a native install smoke job on Windows amd64 and macOS. The branch CI also runs native install smoke: a scratch HOME, source install and repeat update, `--version`, and `providers`. The macOS smoke additionally runs the offline installer e2e group. The Windows workflow runs a focused shell/CLI smoke instead of the Unix-oriented full PTY suite.
- A behavior-test helper now compares words inside one committed TUI block after whitespace normalization. Long temporary paths can wrap the phrase `please read this` across rows without changing the committed prompt; the earlier contiguous-substring assertion timed out despite correct output. Production TUI rendering is unchanged.

## Regression evidence

The distribution tests below were added before their fixes and failed against committed production files. I also temporarily replaced `scripts/install.sh`, `internal/cli/subcommands.go` and both workflows with their HEAD versions after implementation: those tests failed again, then passed after the modified files were restored automatically. Both baseline commands exited 1: doctor ended with `No problems found.`, the installer produced only `kiln` for Windows, and the release workflow had no `verify` or `install-smoke` jobs. The existing TUI test separately failed with long temporary paths before its assertion helper was corrected; captured output showed a valid wrapped `you` block.

| Test | Without fix | With fix |
|---|---|---|
| `TestInstallScript_WindowsReleaseKeepsExeSuffixOnUpdate` | Fails: verified Windows ZIP installs `kiln`, no `kiln.exe` | Passes: installs `kiln.exe`, then replaces it with the next fixture version |
| `TestInstallScript_WindowsSourceKeepsExeSuffix` | Fails: source install produces `kiln`, no `kiln.exe` | Passes: source install produces `kiln.exe` |
| `TestDoctor_UnresolvableModelIsAProblem` | Fails: default and explicit unavailable models end with `No problems found.` | Passes: unavailable/invalid model is listed as a problem, exit remains 0 |
| `TestReleaseWorkflowRequiresVerification` | Fails: tag workflow has no verify job | Passes: tag build depends on checks, publish depends on build and native smoke |
| `TestInstallSmokeRunsOnNativePlatforms` | Fails: neither workflow has native install smoke | Passes: both workflows include Windows and macOS smoke with install and CLI commands |
| `TestHandleSubmit_LeadingPathIsNotACommand` | With long scratch `TMPDIR`, fails on a false negative: the captured `you` block shows `please read` then a line break and `this` | Ten repeats pass after normalization; still checks the words occur in one block, no `Unknown command` appears, and a turn begins |

## Verification and remaining gaps

- Focused installer/workflow e2e tests passed with scratch HOME and local HTTP fixtures; focused doctor test passed. `sh -n scripts/install.sh`, workflow YAML parsing, and `git diff --check` passed.
- Final `make check` passed after the TUI test-only fix with the original long scratch HOME/TMPDIR and normal Go package concurrency (gofmt, vet, pinned staticcheck, unit tests and e2e vet). Earlier runs with the same long temp path failed in `TestHandleSubmit_LeadingPathIsNotACommand` after its two-second print wait. Capturing the fake sink showed the committed prompt was present and wrapped; a long-path ten-repeat run failed before normalization and passed all ten after it. A pre-fix retry with `TMPDIR=/tmp` and `GOFLAGS=-p=1` had passed, but the final run no longer needed those workarounds.
- Final `make e2e` passed in 236.764 seconds after the TUI test-only fix with scratch HOME and local test sockets. The pre-fix full run also passed in 231.545 seconds.
- A macOS scratch-HOME source install and repeat update passed; its installed binary answered `--version` and `providers`. `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath ./cmd/kiln` passed to a scratch `.exe`. These are local checks of the workflow commands and cross-build, not a substitute for native Windows execution.
- The Windows and macOS CI jobs are configured but have not been executed on native runners in this worktree. Published releases and Windows behavior while the executable is open remain unverified locally. The default-model startup logic is owned by a separate runtime worker; this change documents explicit selection and fixes the doctor report but does not alter chat startup.

## Review follow-up: installer cleanup and provider authentication

An independent run of the original distribution commit found that the new Windows source-install test failed during `t.TempDir` cleanup on a fresh module cache. The child install used a scratch product `HOME`, which also became Go's default module-cache root. Go extracts module directories read-only, so the test runner could not remove that HOME. The test now obtains `GOCACHE` and `GOMODCACHE` from the parent `go env` and passes those paths to the source-install child, keeping both caches outside the product HOME's cleanup tree. The test still creates and cleans its scratch HOME and still checks that source installation produces `kiln.exe`.

Doctor now calls the registry's existing `CheckAuth` after a catalog model resolves. An unconfigured provider or failed credential check appears in the problem list with a provider/login diagnostic; the report-only exit status remains 0. The auth error text and credential value are never printed. `AuthKindNone` providers and providers configured through the existing environment or credential store remain accepted.

| Regression | Without fix (reverted file) | With fix |
|---|---|---|
| `TestInstallScript_WindowsSourceKeepsExeSuffix` | Failed at cleanup: `unlinkat .../HOME/go/pkg/mod/gopkg.in/yaml.v3@v3.0.1/decode_test.go: permission denied` (`/private/tmp/kiln-installer-red.log`) | Passed in 4.379 seconds; `t.TempDir` cleanup succeeded |
| `TestDoctor_SelectedCatalogModelRequiresProviderAuth` | Failed: unauthenticated `anthropic/claude-haiku-4-5` resolved to a tier and doctor printed `No problems found.` (`/private/tmp/kiln-doctor-red.log`) | Passed: unauthenticated model is a problem, fake-key authenticated model has no problems, no key appears in output; exit stays 0 |
| `TestDoctor_Runs` | Existing faux provider accepts a local no-auth model | Passed with an added assertion that doctor does not reject `faux/faux-1` |

Both red checks temporarily replaced one file with `git show HEAD:<affected-file>`, ran the named test, and restored the fixed file. The installer red run used the ordinary Go module-cache environment with only a scratch `GOCACHE` override; it reproduced the original cleanup error. The doctor red run used a clean test HOME and no real credential; no model request was made.

Full logs: [`make check`](/private/tmp/kiln-distribution-review-make-check.log) passed (gofmt, vet, staticcheck, unit tests, e2e vet); [`make e2e`](/private/tmp/kiln-distribution-review-make-e2e.log) passed (`go test -tags e2e ./test/e2e/... -count=1 -timeout 10m`, 221.490 seconds). The full e2e command used only a scratch `GOCACHE` override, with no global `GOPATH` or `GOMODCACHE` workaround. Native Windows workflow execution, published release assets, and replacement of an open Windows executable remain unverified locally. The runtime crash-recovery finding is owned by the separate runtime worker and was not changed here.
