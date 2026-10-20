import Foundation

extension Remote {
    public nonisolated func setAuthorityAvailability(active: Bool, protectedDataAvailable: Bool) {
        authorityGate.set(active: active, protectedDataAvailable: protectedDataAvailable)
    }
}
