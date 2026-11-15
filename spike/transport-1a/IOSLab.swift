// Standalone simulator executable. No product dependency or account access.
import Foundation
import UIKit
import WebRTC

final class Observer: NSObject, RTCPeerConnectionDelegate, RTCDataChannelDelegate {
    var candidates: [RTCIceCandidate] = []
    let lock = NSLock()
    let gathered = DispatchSemaphore(value: 0)
    let opened = DispatchSemaphore(value: 0)
    let message = DispatchSemaphore(value: 0)
    var unexpected = 0
    func peerConnection(_ p: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ p: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ p: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ p: RTCPeerConnection) {}
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceConnectionState) {}
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceGatheringState) {
        if newState == .complete { gathered.signal() }
    }
    func peerConnection(_ p: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        lock.lock(); candidates.append(candidate); lock.unlock()
    }
    func peerConnection(_ p: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ p: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {
        unexpected += 1; dataChannel.close()
    }
    func dataChannelDidChangeState(_ dc: RTCDataChannel) { if dc.readyState == .open { opened.signal() } }
    func dataChannel(_ dc: RTCDataChannel, didReceiveMessageWith buffer: RTCDataBuffer) {
        if String(data: buffer.data, encoding: .utf8) == "lab-message" { message.signal() }
    }
}

enum LabError: Error { case timeout, sdk, invalid }
func wait(_ semaphore: DispatchSemaphore) throws {
    if semaphore.wait(timeout: .now() + 10) != .success { throw LabError.timeout }
}
func description(_ pc: RTCPeerConnection, answer: Bool = false, restart: Bool = false) throws -> RTCSessionDescription {
    let done = DispatchSemaphore(value: 0)
    var result: RTCSessionDescription?; var failure: Error?
    let c = RTCMediaConstraints(mandatoryConstraints: restart ? ["IceRestart":"true"] : nil, optionalConstraints: nil)
    let handler: (RTCSessionDescription?, Error?) -> Void = { s, e in result = s; failure = e; done.signal() }
    if answer { pc.answer(for: c, completionHandler: handler) } else { pc.offer(for: c, completionHandler: handler) }
    try wait(done); if let failure { throw failure }; guard let result else { throw LabError.sdk }; return result
}
func install(_ pc: RTCPeerConnection, _ s: RTCSessionDescription, local: Bool) throws {
    try SDPCheck.validate(Data(s.sdp.utf8))
    // Precondition checks on the exact string passed to the SDK; no rebuilding.
    guard s.sdp.hasSuffix("\r\n"), !s.sdp.contains("\0"),
          s.sdp.components(separatedBy: "\r\n").filter({$0.hasPrefix("m=")}).count == 1,
          s.sdp.contains("m=application"), !s.sdp.contains("a=ice-lite") else { throw LabError.invalid }
    let done = DispatchSemaphore(value: 0); var failure: Error?
    let handler: (Error?) -> Void = { e in failure = e; done.signal() }
    if local { pc.setLocalDescription(s, completionHandler: handler) } else { pc.setRemoteDescription(s, completionHandler: handler) }
    try wait(done); if let failure { throw failure }
}
func experiment(_ out: URL) throws {
    RTCInitializeSSL(); RTCSetMinDebugLogLevel(.none)
    let start = ProcessInfo.processInfo.systemUptime
    let factory = RTCPeerConnectionFactory()
    let a = Observer(), b = Observer()
    let config = RTCConfiguration(); config.iceServers = []; config.sdpSemantics = .unifiedPlan
    let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
    guard let pa = factory.peerConnection(with: config, constraints: constraints, delegate: a),
          let pb = factory.peerConnection(with: config, constraints: constraints, delegate: b) else { throw LabError.sdk }
    defer { pa.close(); pb.close(); RTCCleanupSSL() }
    var channels: [RTCDataChannel] = []
    for (pc, observer) in [(pa,a),(pb,b)] {
        for id in 0...1 {
            let c = RTCDataChannelConfiguration(); c.isNegotiated = true; c.channelId = Int32(id); c.isOrdered = id == 0
            if id == 1 { c.maxRetransmits = 0 }
            guard let dc = pc.dataChannel(forLabel: "lab-\(id)", configuration: c) else { throw LabError.sdk }
            dc.delegate = observer; channels.append(dc)
        }
    }
    let offer = try description(pa)
    try Data(offer.sdp.utf8).write(to: out.appendingPathComponent("ios-offer.sdp"))
    try install(pa, offer, local: true); try wait(a.gathered)
    try install(pb, offer, local: false)
    let answer = try description(pb, answer: true)
    try Data(answer.sdp.utf8).write(to: out.appendingPathComponent("ios-answer.sdp"))
    try install(pb, answer, local: true); try wait(b.gathered)
    try install(pa, answer, local: false)
    let exact = pa.remoteDescription?.sdp == answer.sdp && pb.remoteDescription?.sdp == offer.sdp
    for (source, target) in [(a,pb),(b,pa)] {
        source.lock.lock(); let candidates = source.candidates; source.lock.unlock()
        for candidate in candidates {
            let done = DispatchSemaphore(value: 0); var failure: Error?
            target.add(candidate) { e in failure = e; done.signal() }
            try wait(done); if let failure { throw failure }
        }
    }
    try wait(b.opened)
    guard channels[0].sendData(RTCDataBuffer(data: Data("lab-message".utf8), isBinary: true)) else { throw LabError.sdk }
    try wait(b.message)
    let restart = try description(pa, restart: true)
    try Data(restart.sdp.utf8).write(to: out.appendingPathComponent("ios-restart.sdp"))
    let result: [String:Any] = ["sdk":"stasel/WebRTC 157.0.0", "message":true, "negotiated_ids":[0,1],
        "dcep_callbacks":a.unexpected+b.unexpected, "candidates":[a.candidates.count,b.candidates.count],
        "remote_description_matches_before_candidates":exact,
        "elapsed_ms":(ProcessInfo.processInfo.systemUptime-start)*1000]
    try JSONSerialization.data(withJSONObject: result, options: [.prettyPrinted,.sortedKeys]).write(to: out.appendingPathComponent("ios.json"))
}

final class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?
    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options: UIScene.ConnectionOptions) {
        guard let scene = scene as? UIWindowScene else { return }
        window = UIWindow(windowScene: scene); window?.rootViewController = UIViewController(); window?.makeKeyAndVisible()
    }
}
final class AppDelegate: UIResponder, UIApplicationDelegate {
    func application(_ application: UIApplication, configurationForConnecting session: UISceneSession, options: UIScene.ConnectionOptions) -> UISceneConfiguration {
        let config = UISceneConfiguration(name: "Lab", sessionRole: session.role)
        config.delegateClass = SceneDelegate.self
        return config
    }
    func application(_ application: UIApplication, didFinishLaunchingWithOptions options: [UIApplication.LaunchOptionsKey:Any]?) -> Bool {
        DispatchQueue.global().async {
            let out = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            do {
                let config=out.appendingPathComponent("peer.json")
                if FileManager.default.fileExists(atPath:config.path){try crossIOS(JSONSerialization.jsonObject(with:Data(contentsOf:config)) as! [String:Any],out)}else{try experiment(out)}
            } catch {
                let e=error as NSError
                try? Data("\(e.domain):\(e.code)".utf8).write(to: out.appendingPathComponent("failure.txt"))
            }
            try? Data("done".utf8).write(to: out.appendingPathComponent("done"))
        }
        return true
    }
}
@main struct IOSLabMain {
    static func main() { UIApplicationMain(CommandLine.argc, CommandLine.unsafeArgv, nil, NSStringFromClass(AppDelegate.self)) }
}
