import Foundation

extension Optional {
    func unwrap() throws -> Wrapped {
        guard let self else { throw DeviceSecurity.Failure.unavailable }; return self
    }
}
