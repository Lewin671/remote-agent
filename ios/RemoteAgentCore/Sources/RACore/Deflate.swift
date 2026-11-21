import Foundation

/// Maximum decompressed payload size for pairing and current device-security history.
public let maxUnpacked = 64 << 20

/// Inflates raw DEFLATE via clientcore's bounded zlib reader.
func unpack(_ packed: Data, max: Int = maxUnpacked) throws -> Data {
    do {
        return try SecCall.inflateBounded(compressed: packed, limit: max)
    } catch {
        throw OpenError(L10nMessage.errorsIosCryptoEnvelopeDoesNotOpen())
    }
}
