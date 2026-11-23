import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's OriginTest (in QueueTest.kt).
@Suite(.timeLimit(.minutes(2))) struct OriginTests {
    private func meta(_ name: String, _ platform: String, _ description: String) -> PBDeviceMeta {
        .with { $0.name = name; $0.platform = platform; $0.description_p = description }
    }

    @Test func onlySignedClientsAreNamedAndOnlyFromTheDeviceList() {
        let zone = Mk.utc
        func at(_ y: Int, _ m: Int, _ d: Int, _ h: Int, _ min: Int) -> Int64 { Mk.at(y, m, d, h, min, zone: zone) }
        func device(_ id: String = "dddd8a5f", created: Int64, seen: Int64 = 0, _ m: PBDeviceMeta) -> DeviceView {
            DeviceView(id: id, kind: .client, createdMs: created, meta: m, lastSeenMs: seen)
        }
        let chrome = meta("", "web", "Chrome on macOS")
        let browser = device(created: at(2026, 9, 27, 0, 0), chrome)
        #expect(clientName(browser, id: browser.id, among: [:]) == "Chrome · macOS")
        #expect(clientName(device(created: browser.createdMs, meta("", "web", "")), id: browser.id, among: [:]) == L10n.securityBrowser())
        let pixel = device(created: at(2025, 12, 1, 0, 0), meta("我的手机", "android", "Android 16 · Pixel 9"))
        #expect(clientName(pixel, id: pixel.id, among: [:]) == "我的手机")
        #expect(defaultClientName(pixel, id: pixel.id, among: [:]) == "Android · Pixel 9")
        let iphone = device(created: pixel.createdMs, meta("", "ios", "iOS 27.0 · iPhone"))
        #expect(defaultClientName(iphone, id: iphone.id, among: [:]) == "iPhone")
        #expect(clientName(device(created: pixel.createdMs, meta("", "ios", "")), id: iphone.id, among: [:]) == "iOS")
    }

    // --- not in Android's tests ---

    @Test func aDefaultNameTakesTheEndOfTheIdOnlyWhenAnotherClientReadsTheSame() {
        func client(_ id: String, _ name: String, _ platform: String, _ description: String, kind: PBDeviceKind = .client) -> DeviceView {
            DeviceView(id: id, kind: kind, createdMs: 0, meta: meta(name, platform, description))
        }
        let a = client("aaaa1111", "", "ios", "iOS 27.0 · iPhone")
        let b = client("bbbb2222", "", "ios", "iOS 26.4 · iPhone")
        let pad = client("cccc3333", "", "ios", "iOS 27.0 · iPad")
        let web = client("dddd4444", "", "web", "Safari on macOS")
        var all = [a.id: a, pad.id: pad, web.id: web]
        #expect(clientName(a, id: a.id, among: all) == "iPhone")
        #expect(clientName(pad, id: pad.id, among: all) == "iPad")
        #expect(clientName(web, id: web.id, among: all) == "Safari · macOS")
        // A second iPhone: both say which they are.
        all[b.id] = b
        #expect(clientName(a, id: a.id, among: all) == "iPhone · 1111")
        #expect(clientName(b, id: b.id, among: all) == "iPhone · 2222")
        // The default names are what is compared: one given its own name still counts.
        let named = client(b.id, "工作手机", "ios", "iOS 26.4 · iPhone")
        all[b.id] = named
        #expect(clientName(a, id: a.id, among: all) == "iPhone · 1111")
        #expect(clientName(named, id: named.id, among: all) == "工作手机")
        all.removeValue(forKey: b.id)
        #expect(clientName(a, id: a.id, among: all) == "iPhone")
        // Computers do not count.
        all[b.id] = client(b.id, "", "ios", "iOS 26.4 · iPhone", kind: .machine)
        #expect(clientName(a, id: a.id, among: all) == "iPhone")
    }
}
