# Client projection contract, version 1

`vectors.json` is the shared, manually specified oracle for the current event
protocol. Events use protobuf JSON (numeric enums, decimal strings for 64-bit
values); no separate copies or generated expected states live in each client.
`projectionVersion` versions this test contract, not the wire protocol.

Each observation is the domain state after exactly `through` events. `cuts`
are legal batch boundaries: Kotlin and Swift test both `EventReducer` append
and `EventProjection` prefix/tail updates at every cut before the observation,
as well as full replay. Web has a full-replay reducer and checks the same
prefixes and batches. These are in-memory replay tests, **not snapshot restore,
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

Native compaction facts and Android approval associations live in the core
reducer and are consumed by chat rendering. Kotlin's test-only javalite JSON
adapter uses generated builder setters, rejects unknown fields/types, and reads
the same numeric protobuf JSON as Swift/TS. It is not a production decoder.

Run from the repository root:

```
pnpm --dir web exec vitest run src/projection-vectors.test.ts --maxWorkers=2
(cd android && ANDROID_HOME=~/Library/Android/sdk ./gradlew --max-workers=2 -PappIdSuffix=.v2dev :core:test --tests '*ProjectionVectorsTest')
swift test --package-path ios/RemoteAgentCore -j 2 --filter ProjectionVectorsTests
```

`internal/remoteclient` opens and validates events but has no session reducer;
it is deliberately not given a fourth copy of the projection logic. Go tests
validate the JSON corpus against the generated protobuf schema and sequence/cut
boundaries. The shared async-question history observation is also checked through Web
`timeline`, Swift `buildChat`, and Android app `ProjectionRowsVectorsTest`.
UI wording remains in platform tests.

`skeletons.json` contains one manually specified terminal-only skeleton for
all 20 event kinds. Web timeline, Swift chat rows and Android chat rows must
keep every one visible with the same warning; interactive skeletons offer no
remote answer. These fixtures include rewritten ID markers for the two kinds
without a payload text field. This is a rendering/marker contract, not a
transport or real CLI test.
