final class Router: Sendable {
    let path: any Path
    init(path: any Path) { self.path = path }
    func status(_ machineId: String) -> PeerState { path.status(machineId) }
}
