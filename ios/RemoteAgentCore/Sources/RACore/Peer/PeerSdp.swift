import Foundation

enum PeerSdp {
    enum Invalid: Error { case sdp }
    static func matches(_ pattern: String, _ value: String) -> Bool {
        value.range(of: pattern, options: .regularExpression) != nil
    }
    static func validate(_ bytes: Data) throws {
        func require(_ ok: Bool) throws { if !ok { throw Invalid.sdp } }
        try require(!bytes.starts(with: [239,187,191]))
        guard let raw = String(data: bytes, encoding: .utf8) else { throw Invalid.sdp }
        try require(bytes.count <= 60*1024 && raw.hasSuffix("\r\n") && !raw.contains("\0"))
        try require(!raw.unicodeScalars.contains{[11,12,133,8232,8233].contains(Int($0.value))})
        let rest = raw.replacingOccurrences(of: "\r\n", with: "")
        try require(!rest.contains("\r") && !rest.contains("\n"))
        var scopes: [[String:String]] = [[:],[:]], section = 0
        let allowed = Set("group msid-semantic fingerprint ice-ufrag ice-pwd ice-options setup mid sctp-port max-message-size end-of-candidates extmap-allow-mixed sendrecv".split(separator: " ").map(String.init))
        for line in raw.components(separatedBy: "\r\n").dropLast() {
            if line.hasPrefix("m=") {
                section += 1
                try require(section == 1 && matches("^m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel$",line))
            }
            try require(line != "a=ice-lite")
            if line.hasPrefix("a=") {
                let parts = line.dropFirst(2).split(separator: ":",maxSplits: 1,omittingEmptySubsequences: false)
                let key = String(parts[0]), value = parts.count == 2 ? String(parts[1]) : ""
                try require(allowed.contains(key) && scopes[section][key] == nil)
                scopes[section][key] = value
            } else { try require(matches("^[vostcm]=",line)) }
        }
        try require(section == 1)
        let fps = scopes.compactMap { $0["fingerprint"] }
        try require(!fps.isEmpty && fps.allSatisfy { matches("^sha-256 ([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$",$0) })
        try require(Set(fps.map { $0.uppercased() }).count == 1)
        var values: [String:String] = [:]
        for key in ["ice-ufrag","ice-pwd","setup"] {
            let all = scopes.compactMap { $0[key] }
            try require(!all.isEmpty && Set(all).count == 1); values[key] = all[0]
        }
        try require(matches("^[A-Za-z0-9+/]{4,256}$",values["ice-ufrag"]!) && matches("^[A-Za-z0-9+/]{22,256}$",values["ice-pwd"]!))
        try require(["active","passive","actpass"].contains(values["setup"]!))
        let port = scopes[1]["sctp-port"] ?? ""
        try require(matches("^[0-9]+$",port) && (Int(port) ?? 65536) <= 65535)
    }
}

