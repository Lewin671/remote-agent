# Shared Web and iOS authority signing oracle

This test-only Go program covers protocol 10 / state version 3 using fabricated
identities. Both clients call this one program. The `web` and `ios` profiles
retain their original fixed inputs, wire shapes, known answers and scenarios;
no committed vector or existing Go/Android shared vector was changed.

Run from the repository root:

```sh
go run ./testdata/authority-oracle web > web/src/testdata/authority/vectors.json
go run ./testdata/authority-oracle ios > ios/testdata/authority-v3.json
go run ./testdata/authority-oracle web verify < ts-output.json
go run ./testdata/authority-oracle ios verify < swift-output.json
go test ./testdata/authority-oracle
(cd web && pnpm test)
swift test --package-path ios/RemoteAgentCore
```

`web/src/authority.test.ts` regenerates the Web fixture and compares the actual
TS-generated states, leases, recovery statement, normal and replacement
approvals, recovery code and signatures byte for byte. It sends those actual
bytes back to the shared Go verifier; a tampered lease must be rejected.
`membership` and `remove` deliberately produce the same Web vector.

`ios/RemoteAgentCore/Tests/RACoreTests/AuthorityTests.swift` compares Swift output
to the iOS fixture, including signing bytes and serialized bytes with only the
signature replaced by Go's. CryptoKit may randomize Ed25519 signatures; the
actual signatures are independently verified by Go. Approval protobuf bytes
and recovery-code text are compared exactly. The test sends its actual signed
Swift output to the same program using the `ios verify` mode.

## Profiles and coverage

Both profiles invoke production `internal/deviceauth` builders for genesis,
pairing, membership, X rotation, removal, recovery-key rotation (veto), handoff,
recovery and leases. `crypto.go` shares the deterministic grant/PrevSecret
sealing, state signing and independent Crockford recovery-code encoder.
The RFC 9180 base-mode sender uses Go ECDH, HKDF and AES-GCM; only the test
sender inputs/nonces are fixed. Web DHKEM derivation uses `crypto/hpke`; Swift
uses CryptoKit's real sender with a test-only
`HPKEDiffieHellmanPrivateKeyGeneration` implementation. Production random
sources and crypto are unchanged. Every grant is independently opened by
production `deviceauth.Pin.OpenGrant` using `crypto/hpke`.

The union includes Web's Machine and Client pairing, Client X rotation,
clock rollback during rotation/veto/recovery, recovery branching from veto,
normal and machine-replacement approvals, explicit PrevSecret plaintext
checks and persisted-floor lease; and iOS's generic membership addition,
Machine X rotation, pairing clock rollback, linear handoff-then-recovery,
and a lease for every head. Both preserve tombstones, verify genesis and
transitions (including recovery proof), leases and approval signatures.

| Fixed input | Web profile | iOS profile |
|---|---|---|
| Time base (milliseconds) | 1700000000000 | 1700000000000 |
| Signing seeds | 0x11/0x12/0x13/0x14 | 1/2/3/4 |
| Member X inputs | 0x21/0x22/0x23/0x24; rotated 0x25 | seed+10; rotated 70 |
| Recovery seeds | 0x31/0x32/0x33 | 21/22/23 |
| Account key seed / domain | 0x61 / 0x51 | 30 / 50 |
| Content keys | 0x41/0x42/0x43/0x44/0x45/0x45/0x46/0x47 | 40/41/42/43/44/44/45/46 |
| HPKE sender input | IKM 0x71 repeated 32 times, DHKEM-derived | raw X private input 99 repeated 32 times |
| PrevSecret / recovery / approval nonce | 0x72 (12) / 0x73 (32) / 0x74 (16 bytes) | 88 (12/32/16 bytes) |
| Clock cases | rotateX/veto/recovery use 1; lease floor base+100 | pair base-10; later states base+1 through +6; lease floor base+100 |

This is a core interoperability test, not a browser/server/daemon/real-CLI
end-to-end test. The root `testdata` directory is ignored by `go test ./...`
and normal Go package discovery, so run the explicit oracle test above.
It is outside `scripts/release.py`'s `PRODUCT_PATHS`/`PRODUCT_FILES`; it is not
added to any release archive and requires no change to public-scan allowlists.

## Builder reference map

Android paths are relative to
`android/core/src/main/kotlin/io/github/lewin671/remoteagent/core`;
Go paths are relative to `internal/deviceauth` unless qualified. Line numbers
refer to the batch-A baseline. Both TS `Authority` and Swift `Authority` cover
the builders below.

| Builder / primitive | Android core | Go |
|---|---|---|
| newMember / memberWithX | `Authority.kt:47,53` | `state.go:26,527` |
| genesis | `Authority.kt:58` | `state.go:543 BuildGenesis` |
| membership | `Authority.kt:85,263` | `pair.go:45 BuildMembershipState`, `state.go:551 BuildState` |
| pair | `Authority.kt:100` | `pair.go:58 BuildPairState` |
| remove | `Authority.kt:121` | `BuildMembershipState`, `state.go:454 membershipDelta` |
| rotateX | `Authority.kt:139` | `BuildMembershipState`, `membershipDelta` |
| veto | `Authority.kt:159` | `state.go:580 BuildRecoveryKeyRotation` |
| handoff | `Authority.kt:185` | `lease.go:119 BuildHandoff` |
| recovery | `Authority.kt:218` | `recovery.go:134 BuildRecovery` |
| sealGrant / sealPrevSecret | `DeviceSecurity.kt:468,492` | `state.go:489,498,592 sealHPKE`, `state.go:641 sealPrevSecret` |
| RecoveryCode / signRecoveryStatement | `Recovery.kt:15,25,46`, `DeviceSecurity.kt:993` | `recovery.go:104 RecoveryStatement.Sign`; independent test code encoder |
| HeadLease.sign / signLease | `Lease.kt:68` | `lease.go:66 BuildLease` |
| makePairApproval | `Approval.kt:17`, `PairFlow.kt:274,344` | `internal/e2e/approval.go:25 NewPairApprovalReplacing` |
| Signing time / needsRenewal | `Authority.kt:27,28`, `DeviceSecurity.kt:1056` | lease bounds plus caller's persisted floor |
| Local creation / recovery public intake | `Authority.kt:372`, `Recovery.kt:91` | account ID derivation and existing chain verifiers |

## Account-public test scope

`account-public.test.ts` covers validated public-key intake for local creation,
paired registration (including network interruption and retry), and recovery
context (offline genesis prefix, full chain and local pin/extension). Only a
separate public profile is written; the existing account, registration-intent
and security-record formats remain unchanged. The creation factory and
recovery-context intake do not submit or publish anything. Pending operations,
foreground locks, UI, network recovery orchestration, and real CLI acceptance
belong to later batches.

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
