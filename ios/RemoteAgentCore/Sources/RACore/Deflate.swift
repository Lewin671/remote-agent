import Foundation
import zlib

/// Maximum decompressed payload size for pairing and current device-security history.
public let maxUnpacked = 64 << 20

/// Inflates raw DEFLATE. It stops at `max` bytes: a payload that
/// would unpack to more is refused before the rest of it is produced.
func unpack(_ packed: Data, max: Int = maxUnpacked) throws -> Data {
    let bad = OpenError(L10nMessage.errorsIosCryptoEnvelopeDoesNotOpen())
    var z = z_stream()
    guard inflateInit2_(&z, -MAX_WBITS, ZLIB_VERSION, Int32(MemoryLayout<z_stream>.size)) == Z_OK else { throw bad }
    defer { inflateEnd(&z) }
    var out = Data()
    out.reserveCapacity(Swift.min(max, Swift.max(packed.count * 4, 1024)))
    var buf = [UInt8](repeating: 0, count: 64 * 1024)
    var failed = false
    packed.withUnsafeBytes { src in
        z.next_in = UnsafeMutablePointer(mutating: src.bindMemory(to: Bytef.self).baseAddress)
        z.avail_in = uInt(src.count)
        while true {
            let (status, n): (Int32, Int) = buf.withUnsafeMutableBytes { dst in
                z.next_out = dst.bindMemory(to: Bytef.self).baseAddress
                z.avail_out = uInt(dst.count)
                let s = inflate(&z, Z_NO_FLUSH)
                return (s, dst.count - Int(z.avail_out))
            }
            guard status == Z_OK || status == Z_STREAM_END, out.count + n <= max else {
                failed = true
                return
            }
            out.append(contentsOf: buf[0..<n])
            if status == Z_STREAM_END { return }
            // No progress: the data ends before the stream does.
            if n == 0 && z.avail_in == 0 {
                failed = true
                return
            }
        }
    }
    if failed { throw bad }
    return out
}
