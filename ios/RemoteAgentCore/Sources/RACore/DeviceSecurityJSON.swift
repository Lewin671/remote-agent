import Foundation

extension DeviceSecurity {
    static func object(_ value: Any) throws -> [String: Any] {
        guard let o = value as? [String: Any] else { throw Failure.invalid }
        return o
    }
    public static func safeJSONObject(_ data: Data) throws -> Any {
        do {
            return try JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed])
        } catch {
            throw Failure.invalid
        }
    }
}
