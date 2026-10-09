import Foundation
import zlib

/// Packing limits, as in internal/e2e. A payload below `packMin` is sent as
/// it is: packing would save nothing worth the work. One that unpacks to
/// more than `maxUnpacked` is refused, so that a small envelope cannot fill
/// memory.
public let packMin = 256
public let maxUnpacked = 64 << 20

/// `plaintext` as raw DEFLATE (RFC 1951, no zlib or gzip wrapper), or nil when that is not smaller.
func pack(_ plaintext: Data) -> Data? {
    guard !plaintext.isEmpty else { return nil }
    var z = z_stream()
    // Negative window bits: no zlib header or checksum.
    guard deflateInit2_(&z, Z_DEFAULT_COMPRESSION, Z_DEFLATED, -MAX_WBITS, 8, Z_DEFAULT_STRATEGY,
                        ZLIB_VERSION, Int32(MemoryLayout<z_stream>.size)) == Z_OK else { return nil }
    defer { deflateEnd(&z) }
    // Only a result smaller than the input is of use: a buffer of that size is enough to tell.
    var out = [UInt8](repeating: 0, count: plaintext.count)
    let (status, produced): (Int32, Int) = plaintext.withUnsafeBytes { src in
        out.withUnsafeMutableBytes { dst in
            z.next_in = UnsafeMutablePointer(mutating: src.bindMemory(to: Bytef.self).baseAddress)
            z.avail_in = uInt(src.count)
            z.next_out = dst.bindMemory(to: Bytef.self).baseAddress
            z.avail_out = uInt(dst.count)
            let s = deflate(&z, Z_FINISH)
            return (s, dst.count - Int(z.avail_out))
        }
    }
    guard status == Z_STREAM_END, produced < plaintext.count else { return nil }
    return Data(out[0..<produced])
}

/// Inflates what `pack` produced. It stops at `max` bytes: a payload that
/// would unpack to more is refused before the rest of it is produced.
func unpack(_ packed: Data, max: Int = maxUnpacked) throws -> Data {
    let bad = OpenError("envelope does not open")
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
