# Goldens

- Regenerate goldens only for the tests you changed: `UPDATE=1 go test ... -run '<TestName>'`.
  A suite-wide update hides the diff you were supposed to review.
- Read `git diff testdata internal/*/testdata` before committing, and account for every changed
  row. A golden can be captured mid-render (half-drawn prompt options, blank rows): if a
  regenerated golden looks incomplete, restore it with `git checkout` and rerun the test.
- Mask run-varying values (temp paths, timings) in the normalizer, never by loosening the
  assertion.
