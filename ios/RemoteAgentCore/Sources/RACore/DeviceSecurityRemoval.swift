import Foundation

extension DeviceSecurity {
    static func deleteSignBytes(_ r: [String: Any]) throws -> Data {
        let account = try jsonString(r, "Account")
        let requester = try jsonString(r, "Requester")
        let operation = try jsonString(r, "Operation")
        try check(
            identifier(account) && identifier(requester) && opaque(operation)
                && operation.count >= 16)
        let domain = try jsonHash(r["Domain"])
        let expected = try jsonHash(r["Expected"])
        let nonce = try jsonHash(r["Nonce"])
        try nonzero(domain)
        try nonzero(expected)
        try nonzero(nonce)
        guard let exact = r["ExactIDs"] as? Bool, let targets = r["Targets"] as? [[String: Any]],
            !targets.isEmpty, targets.count <= 128
        else { throw Failure.invalid }
        var w = Writer()
        w.raw(Data("ra2/delete\0".utf8))
        w.str(account)
        w.raw(domain)
        w.str(requester)
        w.str(operation)
        w.raw(expected)
        w.raw(nonce)
        w.u32(exact ? 1 : 0)
        w.u32(UInt32(targets.count))
        var previous = ""
        for t in targets {
            let id = try jsonString(t, "ID")
            try check(identifier(id) && (previous.isEmpty || previous < id))
            let commit = try jsonHash(t["MetadataCommit"])
            if !exact { try nonzero(commit) }
            w.str(id)
            w.raw(commit)
            previous = id
        }
        return w.data
    }
    static func deleteDigest(_ r: [String: Any]) throws -> Data {
        try digest("ra2/delete-digest\0", deleteSignBytes(r), jsonBytes(r, "Signature"))
    }
    public static func unnamedMemberCommit(_ state: State, member: Member) throws -> Data {
        var w = Writer()
        w.raw(try stateHash(state))
        w.str(member.id)
        w.raw(member.ed)
        w.raw(member.x)
        w.u32(member.kind)
        return digest("ra2/unnamed-member\0", w.data)
    }
    public static func prepareDelete(
        pin: Pin, requester: String, target: String, expected: Data, metadataCommit: Data,
        signingKey: SigningKey
    ) throws -> Data {
        try check(deviceId(publicKey: signingKey.publicKey) == requester)
        var r: [String: Any] = [
            "Account": pin.account, "Domain": [UInt8](pin.domain), "Requester": requester,
            "Operation": randomBytes(16).hex, "Expected": [UInt8](expected),
            "Nonce": [UInt8](randomBytes(32)), "ExactIDs": false,
            "Targets": [["ID": target, "MetadataCommit": [UInt8](metadataCommit)]],
        ]
        r["Signature"] = signingKey.sign(try deleteSignBytes(r)).base64EncodedString()
        return try JSONSerialization.data(withJSONObject: r)
    }
    static func fenceBytes(_ r: [String: Any]) throws -> Data {
        let account = try jsonString(r, "Account")
        let authority = try jsonString(r, "AuthorityID")
        let executor = try jsonString(r, "Executor")
        let group = try jsonString(r, "Group")
        let revision = try jsonUInt(r, "Revision")
        let epoch = try jsonUInt(r, "Epoch")
        try check(
            identifier(account) && identifier(authority) && identifier(executor) && opaque(group)
                && revision > 0 && revision <= 128 && epoch >= 2)
        let domain = try jsonHash(r["Domain"])
        let state = try jsonHash(r["State"])
        try nonzero(domain)
        try nonzero(state)
        var w = Writer()
        w.raw(Data("ra2/fence-receipt\0".utf8))
        w.str(account)
        w.raw(domain)
        w.str(authority)
        w.str(group)
        w.u64(revision)
        w.u64(epoch)
        w.raw(state)
        w.str(executor)
        return w.data
    }
    public static func verifyCompletion(
        pin: Pin, completionJSON: Data, originalJSON: Data, chain: [State]
    ) throws {
        let c = try object(safeJSONObject(completionJSON))
        let original = try object(safeJSONObject(originalJSON))
        let g = try object(c["Group"] as Any)
        let id = try jsonString(g, "ID")
        guard g["Complete"] as? Bool == true, opaque(id),
            let ops = g["Operations"] as? [[String: Any]],
            !ops.isEmpty, ops.count <= 128, ops.count == chain.count,
            let required = g["Required"] as? [String],
            let receipts = g["Receipts"] as? [[String: Any]]
        else { throw Failure.invalid }
        let base = try stateFromJSON(g["Base"] as Any)
        let head = try stateFromJSON(g["Head"] as Any)
        try verifyState(pin, base)
        try verifyState(pin, head)
        let bh = try stateHash(base)
        let hh = try stateHash(head)
        // Do not shadow the two members in the intersection comparison.
        let actual = base.core.members.filter { m in
            m.kind == 1 && head.core.members.contains(where: { $0.id == m.id })
        }.map(\.id)
        try check(actual == required && receipts.count == required.count, .untrusted)
        var w = Writer()
        w.raw(Data("ra2/completion\0".utf8))
        w.str(pin.account)
        w.raw(pin.domain)
        w.str(pin.authorityID)
        w.str(id)
        w.raw(bh)
        w.raw(hh)
        w.u32(UInt32(ops.count))
        for op in ops {
            let request = try object(op["Request"] as Any)
            w.str(try jsonString(request, "Operation"))
            w.raw(try jsonHash(op["Digest"]))
            w.raw(try jsonHash(op["NewState"]))
        }
        w.u32(UInt32(required.count))
        for (i, r) in receipts.enumerated() {
            let executor = try jsonString(r, "Executor")
            guard executor == required[i],
                let member = head.core.members.first(where: { $0.id == executor }), member.kind == 1
            else { throw Failure.untrusted }
            try check(
                jsonString(r, "Account") == pin.account && jsonHash(r["Domain"]) == pin.domain
                    && jsonString(r, "AuthorityID") == pin.authorityID
                    && jsonString(r, "Group") == id
                    && jsonUInt(r, "Revision") == UInt64(ops.count)
                    && jsonUInt(r, "Epoch") == head.core.epoch
                    && jsonHash(r["State"]) == hh, .untrusted)
            let fb = try fenceBytes(r)
            let signature = try jsonBytes(r, "Signature")
            try check(
                verifyEd25519(publicKey: member.ed, message: fb, signature: signature), .untrusted)
            w.str(executor)
            w.lp(fb)
            w.lp(signature)
        }
        try check(
            verifyEd25519(
                publicKey: pin.authorityEd, message: w.data, signature: jsonBytes(c, "Signature")),
            .untrusted)
        let originalDigest = try deleteDigest(original)
        var before = base
        var found = false
        var removed = Set<String>()
        for (i, op) in ops.enumerated() {
            let next = chain[i]
            let r = try object(op["Request"] as Any)
            try advance(pin, before, next)
            try check(
                stateHash(before) == jsonHash(r["Expected"])
                    && stateHash(next) == jsonHash(op["NewState"])
                    && jsonString(r, "Account") == pin.account
                    && jsonHash(r["Domain"]) == pin.domain,
                .untrusted)
            let requester = try jsonString(r, "Requester")
            guard let member = before.core.members.first(where: { $0.id == requester }) else {
                throw Failure.untrusted
            }
            try check(
                verifyEd25519(
                    publicKey: member.ed, message: deleteSignBytes(r),
                    signature: jsonBytes(r, "Signature")),
                .untrusted)
            let dh = try deleteDigest(r)
            try check(dh == jsonHash(op["Digest"]), .untrusted)
            guard let targets = r["Targets"] as? [[String: Any]] else { throw Failure.invalid }
            let ids = try targets.map { try jsonString($0, "ID") }
            try check(ids == next.core.removed, .untrusted)
            removed.formUnion(ids)
            if try jsonString(r, "Operation") == jsonString(original, "Operation") {
                try check(dh == originalDigest, .untrusted)
                found = true
            }
            before = next
        }
        try check(found && stateHash(before) == hh, .untrusted)
        for id in removed {
            try check(!head.core.members.contains(where: { $0.id == id }), .untrusted)
        }
    }
}
