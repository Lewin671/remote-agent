import Foundation

final class RelayPath: Path {
    private let origin: RelayOrigin
    private let http: HTTP
    private let sleep: Sleeper

    init(origin: RelayOrigin, http: HTTP, sleep: @escaping Sleeper) {
        self.origin = origin
        self.http = http
        self.sleep = sleep
    }

    func openStream(_ route: String, hello: Data) -> any FrameStream {
        Socket(http: http, url: origin.socketURL(route), hello: hello, sleep: sleep)
    }
}
