import Foundation

/// A recovery credential carries a seed and the first ten genesis-hash bytes.
/// Like Android's format it has zero padding, but no checksum. Never persist it.
public enum RecoveryCode {
  private static let alphabet = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ".utf8)
  public struct Decoded: Sendable, CustomStringConvertible, CustomDebugStringConvertible,
    CustomReflectable
  {
    public let seed: Data
    public let genesis: Data
    public var description: String { "RecoveryCode.Decoded" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
  }
  public static func encode(seed: Data, genesis: Data) throws -> String {
    try DeviceSecurity.check(seed.count == 32 && genesis.count == 32)
    var value: UInt32 = 0
    var bits = 0
    var out = [UInt8]()
    for b in genesis.prefix(10) + seed {
      value = (value << 8) | UInt32(b)
      bits += 8
      while bits >= 5 {
        bits -= 5
        out.append(alphabet[Int((value >> bits) & 31)])
      }
    }
    if bits > 0 { out.append(alphabet[Int((value << (5 - bits)) & 31)]) }
    return stride(from: 0, to: out.count, by: 4).map {
      String(decoding: out[$0..<min($0 + 4, out.count)], as: UTF8.self)
    }.joined(separator: "-")
  }
  public static func decode(_ text: String) -> Decoded? {
    var value: UInt32 = 0
    var bits = 0
    var out = Data()
    for raw in text.trimmingCharacters(in: .whitespacesAndNewlines).uppercased().utf8 {
      if [32, 45, 10, 13].contains(raw) { continue }
      let c: UInt8 = raw == 79 ? 48 : (raw == 73 || raw == 76 ? 49 : raw)
      guard let v = alphabet.firstIndex(of: c) else { return nil }
      value = (value << 5) | UInt32(v)
      bits += 5
      if bits >= 8 {
        bits -= 8
        out.append(UInt8((value >> bits) & 255))
        if out.count > 42 { return nil }
      }
    }
    guard out.count == 42, bits < 5, value & ((1 << bits) - 1) == 0 else { return nil }
    return Decoded(seed: out.suffix(32), genesis: out.prefix(10))
  }
}
