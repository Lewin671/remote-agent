import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core QueueTest.
@Suite struct QueueTests {
    @Test func theQueueKeepsTheDaemonsOrder() {
        let d = derive([
            Mk.ev(1, Mk.inputState("b", .queued)),
            Mk.ev(2, Mk.inputState("a", .queued)),
            Mk.ev(3, Mk.inputState("c", .queued)),
            Mk.ev(4, Mk.inputState("a", .canceled)),
        ])
        #expect(queued(d) == ["b", "c"])
    }

    @Test func theDaemonsReasonComesFirst() {
        let s = Mk.session { $0.inputWait = "draft"; $0.agent = .codex }
        #expect(queueWhy(s, status: .running).short == "终端里有草稿")
        let r = Mk.session()
        #expect(queueWhy(r, status: .running).short == "本轮结束后发送")
        #expect(queueWhy(r, status: .running).long == "等待本轮结束")
    }

    @Test func aCancelOutcomeSaysWhoCanceled() {
        #expect(cancelOutcome(status: .canceled, canceledHere: true) == .canceled)
        #expect(cancelOutcome(status: .canceled, canceledHere: false) == .canceledElsewhere)
        #expect(cancelOutcome(status: .submitted, canceledHere: true) == .written)
    }
}

/// Android's OriginTest (in QueueTest.kt).
@Suite struct OriginTests {
    private func o(_ kind: PBOriginKind, _ dev: String = "") -> PBOrigin { .with { $0.kind = kind; $0.deviceID = dev } }

    private func meta(_ name: String, _ platform: String, _ description: String) -> PBDeviceMeta {
        .with { $0.name = name; $0.platform = platform; $0.description_p = description }
    }

    @Test func onlySignedClientsAreNamedAndOnlyFromTheDeviceList() {
        let phone = DeviceView(id: "aaaab71c", kind: .client, createdMs: 0, meta: .with { $0.platform = "android" })
        let named = DeviceView(id: "cccc92de", kind: .client, createdMs: 0, meta: .with { $0.name = "我的手机" })
        var state = RemoteState()
        state.devices = [phone.id: phone, named.id: named]
        // This client's own doings are not labelled.
        #expect(originLabel(o(.client, "me"), state: state, me: "me", machine: "MacBook") == nil)
        #expect(originLabel(o(.client, "aaaab71c"), state: state, me: "me", machine: "MacBook") == "Android · b71c")
        #expect(originLabel(o(.client, "cccc92de"), state: state, me: "me", machine: "MacBook") == "我的手机")
        // Removed: no part of its ID is shown.
        #expect(originLabel(o(.client, "gone9999"), state: state, me: "me", machine: "MacBook") == "已移除的设备")
        #expect(originLabel(o(.client, ""), state: state, me: "me", machine: "MacBook") == "另一客户端")
        // Before the device list is read nothing is known of who it was.
        #expect(originLabel(o(.client, "gone9999"), state: RemoteState(), me: "me", machine: "MacBook") == "另一客户端")
        let zone = Mk.utc
        func at(_ y: Int, _ m: Int, _ d: Int, _ h: Int, _ min: Int) -> Int64 { Mk.at(y, m, d, h, min, zone: zone) }
        let now = at(2026, 10, 3, 12, 0)
        func device(_ id: String = "dddd8a5f", created: Int64, seen: Int64 = 0, _ m: PBDeviceMeta) -> DeviceView {
            DeviceView(id: id, kind: .client, createdMs: created, meta: m, lastSeenMs: seen)
        }
        let chrome = meta("", "web", "Chrome on macOS")
        let browser = device(created: at(2026, 9, 27, 0, 0), chrome)
        #expect(clientName(browser, id: browser.id) == "Chrome · 8a5f")
        #expect(clientName(device(created: browser.createdMs, meta("", "web", "")), id: browser.id) == "浏览器 · 8a5f")
        #expect(clientDetail(browser) == "浏览器 · Chrome on macOS")
        let pixel = device(created: at(2025, 12, 1, 0, 0), meta("我的手机", "android", "Android 16 · Pixel 9"))
        #expect(clientName(pixel, id: pixel.id) == "我的手机")
        #expect(defaultClientName(pixel, id: pixel.id) == "Android · 8a5f")
        #expect(clientDetail(pixel) == "App · Android 16 · Pixel 9")
        let iphone = device(created: pixel.createdMs, meta("", "ios", "iOS 27.0 · iPhone"))
        #expect(defaultClientName(iphone, id: iphone.id) == "iOS · 8a5f")
        #expect(clientDetail(iphone) == "App · iOS 27.0 · iPhone")
        #expect(clientName(device(created: pixel.createdMs, meta("", "ios", "")), id: iphone.id) == "iOS · 8a5f")
        #expect(clientDetail(nil) == "种类未知")
        func used(_ seen: Int64) -> String { clientUsed(device(created: browser.createdMs, seen: seen, chrome), now: now, zone: zone) }
        #expect(used(now - 30_000) == "刚刚使用")
        #expect(used(now - 5 * 60_000) == "5 分钟前使用")
        #expect(used(at(2026, 10, 3, 9, 5)) == "今天 9:05 使用")
        #expect(used(at(2026, 10, 2, 23, 0)) == "昨天使用")
        #expect(used(at(2026, 8, 3, 0, 0)) == "8月3日使用")
        #expect(clientUsed(pixel, now: now, zone: zone) == "2025年12月1日添加")
        #expect(clientUsed(nil, now: now, zone: zone) == "已配对")
        func c(_ id: String, _ created: Int64, _ seen: Int64) -> DeviceView { device(id, created: created, seen: seen, chrome) }
        #expect(sortClients([c("old", 1, 0), c("me", 2, 5), c("new", 3, 0), c("recent", 1, 9), c("stale", 1, 4)], me: "me").map(\.id) == ["me", "recent", "stale", "new", "old"])
        #expect(originLabel(o(.local), state: state, me: "me", machine: "MacBook") == "MacBook 终端")
        #expect(originLabel(o(.local), state: state, me: "me", machine: "") == "终端")
        #expect(originLabel(nil, state: state, me: "me", machine: "MacBook") == nil)
    }

    // --- not in Android's tests ---

    @Test func aDescriptionThatStartsWithADigitIsNoPlatformName() {
        #expect(platformLabel(meta("", "android", "16 · Pixel 9")) == "Android")
        // A digit of any script, as Kotlin's isDigit.
        #expect(platformLabel(meta("", "web", "٣ tabs")) == "浏览器")
        #expect(platformLabel(meta("", "", "")) == "客户端")
        #expect(platformLabel(nil) == "客户端")
        // The first word ends at ASCII whitespace.
        #expect(platformLabel(meta("", "web", "  Safari\ton iOS")) == "Safari")
    }

    @Test func clientsEqualInEverythingGoByIdWhateverOrderTheyComeIn() {
        func c(_ id: String) -> DeviceView { DeviceView(id: id, kind: .client, createdMs: 7, lastSeenMs: 0) }
        #expect(sortClients([c("b"), c("c"), c("a")], me: "x").map(\.id) == ["a", "b", "c"])
        #expect(sortClients([c("c"), c("a"), c("b")], me: "x").map(\.id) == ["a", "b", "c"])
    }
}

/// Android's AcceptedTest (in QueueTest.kt).
@Suite struct AcceptedTests {
    private let local = PBOrigin.with { $0.kind = .local }
    private let client = PBOrigin.with { $0.kind = .client; $0.deviceID = "d1" }

    @Test func everyClientSeesWhatWasAcceptedAndHowFarItGot() throws {
        let d = derive([
            Mk.ev(1, Mk.inputState("r", .queued) { $0.text = "看看这两张"; $0.imageIds = ["i1", "i2"]; $0.stage = .waiting }, origin: client),
            Mk.ev(2, Mk.inputState("r", .queued) { $0.stage = .pastingImages }),
            Mk.ev(3, Mk.inputState("r", .failed, reason: "image_not_recognized")),
        ])
        let a = try #require(d.accepted["r"])
        #expect(a.text == "看看这两张")
        #expect(a.imageIds == ["i1", "i2"])
        #expect(a.origin?.deviceID == "d1")
        #expect(a.stage == .pastingImages)
        #expect(d.failures["r"] == "image_not_recognized")
        #expect(queued(d) == [])
        #expect(failureText(reason: "image_not_recognized", agent: .claude) == "Claude Code 没认出图片")
    }

    @Test func theOriginSaysWhatWasTypedInTheTerminal() {
        let d = derive([Mk.ev(1, Mk.user("typed"), origin: local), Mk.ev(2, Mk.user("sent"), origin: client)], agent: .claude)
        #expect(d.typedLocally == [1])
    }
}
