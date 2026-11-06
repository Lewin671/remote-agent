import SwiftUI

public extension EnvironmentValues {
    /// A process-local integer assigned by the chat row. Never an event/session ID.
    @Entry var chatDiagnosticEntity: UInt64 = 0
}
