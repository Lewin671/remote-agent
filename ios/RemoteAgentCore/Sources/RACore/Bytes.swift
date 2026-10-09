import CryptoKit
import Foundation

public let keySize = 32

public func randomBytes(_ n: Int) -> Data {
    var g = SystemRandomNumberGenerator()
    return Data((0..<n).map { _ in UInt8.random(in: .min ... .max, using: &g) })
}

extension Data {
    public var hex: String {
        let digits = Array("0123456789abcdef".utf8)
        var out = [UInt8]()
        out.reserveCapacity(count * 2)
        for b in self {
            out.append(digits[Int(b >> 4)])
            out.append(digits[Int(b & 0xf)])
        }
        return String(decoding: out, as: UTF8.self)
    }

    /// The bytes a hex string spells, or nil when it is not hex.
    public init?(hex: String) {
        let s = Array(hex.utf8)
        guard s.count % 2 == 0 else { return nil }
        func nibble(_ c: UInt8) -> UInt8? {
            switch c {
            case 0x30...0x39: return c - 0x30
            case 0x61...0x66: return c - 0x61 + 10
            case 0x41...0x46: return c - 0x41 + 10
            default: return nil
            }
        }
        var out = [UInt8]()
        out.reserveCapacity(s.count / 2)
        for i in stride(from: 0, to: s.count, by: 2) {
            guard let hi = nibble(s[i]), let lo = nibble(s[i + 1]) else { return nil }
            out.append(hi << 4 | lo)
        }
        self.init(out)
    }
}

public func sha256(_ parts: Data...) -> Data {
    var h = SHA256()
    for p in parts { h.update(data: p) }
    return Data(h.finalize())
}

/// HKDF-SHA256 with the given salt and info.
public func hkdf(secret: Data, salt: Data?, info: Data, size: Int) -> Data {
    let key = HKDF<SHA256>.deriveKey(
        inputKeyMaterial: SymmetricKey(data: secret), salt: salt ?? Data(), info: info, outputByteCount: size)
    return key.withUnsafeBytes { Data($0) }
}

func u32be(_ n: Int) -> Data {
    let v = UInt32(n)
    return Data([UInt8(v >> 24), UInt8(v >> 16 & 0xff), UInt8(v >> 8 & 0xff), UInt8(v & 0xff)])
}
