import Foundation

extension DeviceSecurity {
    static func object(_ value: Any) throws -> [String: Any] {
        guard let o = value as? [String: Any] else { throw Failure.invalid }
        return o
    }
}
