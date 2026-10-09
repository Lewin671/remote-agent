import Foundation

/// The repository root, found from this file: the vectors are shared with Go, TypeScript and Kotlin.
let repoRoot: URL = {
    var u = URL(fileURLWithPath: #filePath)
    // .../ios/RemoteAgentCore/Tests/RACoreTests/Fixtures.swift
    for _ in 0..<5 { u.deleteLastPathComponent() }
    return u
}()

func loadJSON(_ path: String) throws -> [String: Any] {
    let data = try Data(contentsOf: repoRoot.appendingPathComponent(path))
    return try JSONSerialization.jsonObject(with: data) as! [String: Any]
}

extension Dictionary where Key == String, Value == Any {
    func s(_ k: String) -> String { self[k] as! String }
    func o(_ k: String) -> [String: Any] { self[k] as! [String: Any] }
    func bytes(_ k: String) -> Data { Data(hex: s(k))! }
}
