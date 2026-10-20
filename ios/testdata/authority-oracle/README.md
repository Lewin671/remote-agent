# iOS authority signing oracle

This is test-only code. Run from the repository root:

```sh
go run ./ios/testdata/authority-oracle > ios/testdata/authority-v3.json
swift test --package-path ios/RemoteAgentCore --filter AuthorityTests
```

`AuthorityTests` passes Swift's actual signed output on stdin to:

```sh
go run ./ios/testdata/authority-oracle verify
```

The oracle uses production `internal/deviceauth` builders for genesis, pairing,
membership, X rotation/removal (membership), recovery-key rotation, handoff,
recovery and leases. It fixes only the randomized grant/PrevSecret/proof fields,
then signs with Go Ed25519. Its test-only RFC 9180 sealer fixes X25519 ephemeral
input without changing production randomness; Go's production HPKE recipient
also opens every grant. Swift uses CryptoKit's real HPKE sender with a test-only
`HPKEDiffieHellmanPrivateKeyGeneration` implementation.

All timestamps are milliseconds: base time 1700000000000, pairing uses base-10
(to test clamping to the parent), subsequent operations use base+1 through +6.
Leases use wall clock base-100 and floor base+100. Signing seeds are repeated
bytes 1/2/3/4, member X inputs are seed+10 (rotated X is byte 70), recovery seeds
are 21/22/23, account public-key seed is 30, domain byte 50, content keys are
40/41/42/43/44/44/45/46. Nonces use byte 88 (12/16/32 bytes); the HPKE ephemeral
X input is byte 99 repeated 32 times. All keys are synthetic.

For every state, lease and recovery statement, tests compare the signing bytes
and the complete serialized bytes with only the signature replaced by Go's.
CryptoKit may randomize Ed25519 signatures, so they are independently checked
using Go's production genesis/transition/lease/recovery verifiers. Approval
protobuf bytes are compared exactly; `e2e.VerifyPairApproval` checks its actual
Swift signature. The recovery-code text is also compared exactly.

The existing Go/Android shared vectors are read-only. No product protocol,
serialization label, state format or existing stored record was changed.

## Reference map

`A` is `android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/`;
`G` is `internal/deviceauth/`. Line numbers refer to the v0.44.0 baseline.

| Swift builder/primitive | Android reference | Go reference |
|---|---|---|
| newMember / memberWithX | A/Authority.kt:47,53 | G/state.go:26,527 |
| genesis | A/Authority.kt:58 | G/state.go:543 |
| membership | A/Authority.kt:85,263 | G/pair.go:45; G/state.go:551 |
| pair | A/Authority.kt:100 | G/pair.go:58 |
| remove | A/Authority.kt:121 | G/pair.go:45; G/state.go:454,551 |
| rotateX | A/Authority.kt:139 | G/pair.go:45; G/state.go:454,551 |
| veto | A/Authority.kt:159 | G/state.go:580 |
| handoff | A/Authority.kt:185 | G/lease.go:119 |
| recovery | A/Authority.kt:218 | G/recovery.go:134 |
| sealGrant | A/DeviceSecurity.kt:468 | G/state.go:489,498,592 |
| sealPrevSecret (existing; nonce injectable) | A/DeviceSecurity.kt:492 | G/state.go:641 |
| RecoveryStatement.signed | A/DeviceSecurity.kt:993 | G/recovery.go:104 |
| RecoveryCode encode/decode | A/Recovery.kt:25,46 | Test oracle's independent Crockford encoder (no Go production codec) |
| HeadLease.sign | A/Lease.kt:68 | G/lease.go:66 |
| makePairApproval | A/Approval.kt:17; A/PairFlow.kt:344 | internal/e2e/approval.go:25 |
| needsRenewal | A/Authority.kt:28 | Lease bounds in G/lease.go |

## Scope of account-public material

`AccountPublicProfile.created` derives and checks the account ID against the
verified genesis. `RegistrationIntent.publicProfile` extracts and validates the
key from the existing exact registration request. The app saves that profile
before installing the pairing bootstrap, including on registration retry, and
keeps it after clearing the intent. `AccountPublicProfile.recovery` checks the
account key ID, full chain, genesis prefix and any locally accepted pin/chain.
Creation/recovery callers use `Vault.saveAccountPublicProfile` before adoption;
the network coordinators and their UI belong to later batches and are not added
here. Profiles are immutable separate Keychain entries, bound to account and
genesis, with WhenUnlockedThisDeviceOnly and synchronizable=false. Missing data
is explicit; corrupted or conflicting data throws. No migration is performed.
