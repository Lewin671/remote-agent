package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.MachineReplacementProof

/**
 * Finds all historical machine IDs allowed to sign for [wantMachine] across replacement
 * approvals anchored in the verified security chain, delegated to Go `clientcore` `SecCall`.
 */
fun machineReplacementOrigins(
    record: DeviceSecurity.Record,
    wantMachine: String,
    replacements: List<MachineReplacementProof>,
): Set<String> =
    SecCall.machineReplacementOrigins(
        account = record.pin.account,
        targetMachineId = wantMachine,
        states = record.chain.map { DeviceSecurity.stateBytes(it) },
        proofs = replacements,
    )
