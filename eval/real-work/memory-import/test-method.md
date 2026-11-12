# Hidden acceptance method

`hidden_acceptance.py` runs the task contract through the compiled `kiln memory`
CLI. It uses only Python's standard library and Git. Each named case creates a
fresh temporary HOME and Git project, then emits one JSON result. It never calls
a model or network service. Run it after a candidate patch is frozen:

```sh
python3 eval/real-work/memory-import/hidden_acceptance.py --binary /absolute/path/to/kiln \
  > hidden-results.jsonl
```

The `existing_workflow` case checks that baseline remember, global remember,
export, and scope isolation work. Every import case first checks that an empty
valid array imports successfully. This prevents a missing import command from
making rejection cases pass vacuously. On baseline commit `40e9464`, an isolated
build passes `existing_workflow` and fails all 28 import cases as expected.

The checks are black box except for deliberate SQLite snapshots and injected
write-failure triggers. They compare `memories`, `memory_sources`, and
`memory_search` before and after rejected, dry-run, cancelled, and failed-write
imports. The cancellation case terminates an import while another connection
holds a database write lock; it verifies no database change but does not prove
rollback after an in-flight row write. Source tests use a nonexistent original
machine path and a destination fixture to detect attempts to read the origin.
The injected memory-write failure occurs after one new row has been inserted,
independent of the order of the two selected keys. A separate replacement
failure checks restoration of an existing active row. The suite inspects FTS
contents after those failures but does not inject a fault inside SQLite's FTS
virtual-table implementation.

The suite tests the explicit task contract, not internal function names or a
particular Go design. It does not try to measure timing, provider usage, or
whether a candidate's documentation and unit tests are sufficient; those are
separate rubric dimensions. A failure is reported independently even if an
earlier case also failed. The hidden suite must remain outside candidate
checkouts until their patches are frozen.
