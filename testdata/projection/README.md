# Client projection contract, version 1

`vectors.json` is the shared, manually specified oracle for the current event
protocol. Events use protobuf JSON (numeric enums, decimal strings for 64-bit
values); no separate copies or generated expected states live in each client.
`projectionVersion` versions this test contract, not the wire protocol.

Each observation is the domain state after exactly `through` events. `cuts`
are legal batch boundaries. The shared Go core (`clientcore/projection`), which
all three clients call, is tested for full replay and for incremental appends
at every cut before the observation. These are in-memory replay tests, **not snapshot restore,
encryption, transport, or real CLI tests**.

Normalization compares pending IDs in insertion order; inputs in acceptance
order with numeric status, retained stage/mode and current pause flag; accepted
text/images/origin; queued IDs, input failure reasons and the latest plan; decisions and answers; running, completed, unknown and
interrupted tool starts; current turn background tools; local-source annotations;
compaction percentages (`null` = unknown); terminal configuration change event
IDs; unambiguous approval/call associations. Sequences are decimal strings;
A background result without a matching start uses the result event as its known
start; it does not consume another tool. Sequence arrays are numerically sorted, ID sets lexically sorted, maps compared
without key order. `local` is an annotation, not a fabricated device identity.
Unknown origin is `0`. No layout or translated display strings are expected.

Native compaction facts and approval associations live in the core reducer
and are consumed by each client's chat rendering.

Run from the repository root:

```
(cd clientcore && go test ./projection/)
```

The core test reads the protobuf JSON with protojson and feeds the core the
standard wire encoding, so it also checks that the core's reflection-free types
read what the clients' protobuf libraries write. The shared async-question
history observation is also checked through Web `timeline`, Swift `buildChat`,
and Android app `ProjectionRowsVectorsTest`. UI wording remains in platform tests.

`skeletons.json` contains one manually specified terminal-only skeleton for
all 20 event kinds. Web timeline, Swift chat rows and Android chat rows must
keep every one visible with the same warning; interactive skeletons offer no
remote answer. These fixtures include rewritten ID markers for the two kinds
without a payload text field. This is a rendering/marker contract, not a
transport or real CLI test.

`live-inputs.json` supplies history + an authenticated live snapshot + manually
specified merged inputs to the core's `CurrentActions`. It covers bounded
queued/held previews, retained text, images, files and sender, explicit false
pause, sparse stage/mode, missing history/live entries, and history newer than
the snapshot watermark. The closed plan case has no pending plan reference.
These are merge tests, not transport tests. The Go core test and Web
`current-derived.test.ts` (which goes through the WASM) run them.
