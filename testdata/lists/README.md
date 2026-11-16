# Client lists & config shared contract, version 1

`vectors.json` is the shared, manually specified oracle for `clientcore/lists`
and `clientcore/outcome` (step 3a of the shared Go core migration).

Inputs use protobuf JSON (numeric or symbolic enums, decimal strings for 64-bit
integers) decoded through standard protobuf and re-encoded to wire bytes before
passing into `clientcore`, verifying both business rules and `protoc-gen-go-lite`
wire compatibility.

Calendar grouping tests pass `LocalCalendar` with explicit `utc_offset_minutes`
(e.g. `480` for `Asia/Shanghai`) or explicit local midnight boundaries
(`today_start_ms`, `yesterday_start_ms`, `week_start_ms`) so the Go core stays
independent of `time/tzdata`.

Run from the repository root:

```sh
(cd clientcore && go test ./lists/ ./outcome/ ./corebind/)
```
