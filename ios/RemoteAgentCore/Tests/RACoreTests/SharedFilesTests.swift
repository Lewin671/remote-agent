import Foundation
import RAProto
import Testing

@testable import RACore

/// What a card says and does, by state; the blocks' arithmetic against the vectors Go, the web and Android share.
@Suite struct SharedFilesTests {
    private func zh<T>(_ body: () -> T) -> T { MessageLocale.$overrideForTesting.withValue("zh-CN", operation: body) }
    private func en<T>(_ body: () -> T) -> T { MessageLocale.$overrideForTesting.withValue("en", operation: body) }

    // MARK: Names

    @Test func kindsFollowTheExtension() {
        #expect(classifySharedFile("daocheng-final.MP4") == .video)
        #expect(classifySharedFile("cover.png") == .image)
        #expect(classifySharedFile("a.m4a") == .audio)
        #expect(classifySharedFile("project.zip") == .archive)
        #expect(classifySharedFile("sample.pdf") == .pdf)
        #expect(classifySharedFile("notes.md") == .doc)
        #expect(classifySharedFile("Makefile") == .other)
        #expect(classifySharedFile(".mp4") == .other)
        #expect(SharedFileKind.doc.word == zh { L10n.sharedFileTypeOther() })
    }

    @Test func aNameFromTheComputerIsMadeSafe() {
        #expect(sanitizeSharedFileName("../../etc/passwd") == "passwd")
        #expect(sanitizeSharedFileName("C:\\Users\\me\\a.txt") == "a.txt")
        #expect(sanitizeSharedFileName("a\u{0}b\u{7}c.txt") == "abc.txt")
        // Bidirectional controls would make "evil\u{202E}gpj.exe" read as a picture.
        #expect(sanitizeSharedFileName("evil\u{202E}gpj.exe") == "evilgpj.exe")
        #expect(sanitizeSharedFileName("\u{2066}x\u{2069}.txt") == "x.txt")
        #expect(sanitizeSharedFileName("") == "file")
        #expect(sanitizeSharedFileName("..") == "file")
        #expect(sanitizeSharedFileName("  ") == "file")
        let long = String(repeating: "长", count: 300) + ".mp4"
        let cut = sanitizeSharedFileName(long)
        #expect(cut.count == 200 && cut.hasSuffix(".mp4"))
    }

    @Test func aLongNameKeepsItsEndingWhenCutInTheMiddle() {
        let (head, tail) = splitMiddleTruncateName("daocheng-final.mp4")
        #expect(head + tail == "daocheng-final.mp4")
        #expect(tail == "nal.mp4")
        #expect(splitMiddleTruncateName("README") == ("README", ""))
        #expect(splitMiddleTruncateName(".gitignore") == (".gitignore", ""))
    }

    @Test func theHomeFolderIsATilde() {
        #expect(displaySharedPath("/Users/me/Movies/daocheng/out/a.mp4", home: "/Users/me", fallbackName: "a.mp4") == "~/Movies/daocheng/out/a.mp4")
        #expect(displaySharedPath("/Users/me/Movies/a.mp4", home: "", fallbackName: "a.mp4") == "~/Movies/a.mp4")
        #expect(displaySharedPath("/home/dev/x.txt", home: "", fallbackName: "x.txt") == "~/x.txt")
        #expect(displaySharedPath("/tmp/x.txt", home: "/Users/me", fallbackName: "x.txt") == "/tmp/x.txt")
        #expect(displaySharedPath("  ", home: "", fallbackName: "../x.txt") == "x.txt")
    }

    // MARK: Numbers

    @Test func sizesProgressAndDurationsRead() {
        #expect(sharedFileSize(Int64(24.3 * 1024 * 1024)) == "24.3 MB")
        let p = sharedFileProgress(done: Int64(8.1 * 1024 * 1024), total: Int64(24.3 * 1024 * 1024))
        #expect(p.done == "8.1" && p.total == "24.3 MB")
        let mixed = sharedFileProgress(done: 900 * 1024, total: Int64(2.5 * 1024 * 1024))
        #expect(mixed.done == "900.0 KB" && mixed.total == "2.5 MB")
        #expect(sharedFileDuration(ms: 102_000) == "1:42")
        #expect(sharedFileDuration(ms: 3_725_000) == "1:02:05")
        #expect(sharedFileDuration(ms: 499) == "0:00")
        zh {
            #expect(sharedFileEta(seconds: 12) == "12 秒")
            #expect(sharedFileEta(seconds: 61) == "2 分钟")
            #expect(sharedFileEta(seconds: 0) == "1 秒")
        }
        en {
            #expect(sharedFileEta(seconds: 1) == "1 sec")
            #expect(sharedFileEta(seconds: 90) == "2 mins")
        }
    }

    // MARK: Blocks

    @Test func theBlocksAreThoseOfTheSharedVectors() throws {
        let v = try loadJSON("testdata/shared-files/blocks.json")
        let block = Int(v.s("blockBytes"))!
        #expect(block == defaultSharedFileBlockBytes)
        for c in v["cases"] as! [[String: Any]] {
            let size = Int64(c.s("sizeBytes"))!
            let plan = planSharedFileBlocks(size: size, blockBytes: block)
            #expect(plan.offsets == (c["offsets"] as! [String]).map { Int64($0)! }, "\(c.s("name"))")
            #expect(plan.lastBlockFileBytes == Int(c.s("lastBlockFileBytes"))!, "\(c.s("name"))")
            for o in plan.offsets { #expect(isValidSharedFileOffset(o, size: size, blockBytes: block)) }
            for o in (c["invalidOffsets"] as! [String]).map({ Int64($0)! }) {
                #expect(!isValidSharedFileOffset(o, size: size, blockBytes: block), "\(c.s("name")) \(o)")
            }
        }
    }

    @Test func theMachineNamesItsBlockSize() {
        var m = MachineView(id: "m", online: true, lastSeenMs: 0, meta: PBMachineMeta())
        #expect(sharedFileBlockBytes(m) == 786_432)
        m.meta?.capabilities = .with { $0.sharedFiles = true; $0.sharedFileChunkBytes = 4096 }
        #expect(sharedFileBlockBytes(m) == 4096)
        #expect(sharedFilesSupported(m))
        m.meta?.capabilities.sharedFiles = false
        #expect(!sharedFilesSupported(m))
        #expect(!sharedFilesSupported(nil))
    }

    // MARK: The card

    private let paused = SharedFileTransfer.paused(done: 8_000, total: 24_000)
    private let gone = URL(fileURLWithPath: "/tmp/a.mp4")

    @Test func aCopyOnThePhoneBeatsEverything() {
        let t = SharedFileTransfer.downloaded(url: gone, durationMs: 102_000)
        #expect(sharedFileCard(t, machineOnline: false, sessionExited: true) == .downloaded(durationMs: 102_000))
    }

    @Test func aDownloadInFlightIsShownAsOneEvenWithTheComputerAway() {
        let t = SharedFileTransfer.downloading(done: 1, total: 4, etaSeconds: nil, waitingReconnect: true)
        #expect(sharedFileCard(t, machineOnline: false, sessionExited: false) == .downloading(done: 1, total: 4, etaSeconds: nil))
    }

    @Test func pausedStaysPausedWhenTheComputerGoesAndComesBack() {
        #expect(sharedFileCard(paused, machineOnline: false, sessionExited: false) == .paused(done: 8_000, total: 24_000))
        #expect(sharedFileCard(paused, machineOnline: true, sessionExited: false) == .paused(done: 8_000, total: 24_000))
    }

    @Test func anEndedSessionClearsWhatWasNeverFetched() {
        #expect(sharedFileCard(.idle, machineOnline: true, sessionExited: true) == .cleaned)
        #expect(sharedFileCard(.cleanedUp, machineOnline: true, sessionExited: false) == .cleaned)
        #expect(sharedFileCard(.failed(.disconnected), machineOnline: true, sessionExited: true) == .cleaned)
    }

    @Test func anAbsentComputerIsOfflineUntilItIsBack() {
        #expect(sharedFileCard(.idle, machineOnline: false, sessionExited: false) == .offline)
        #expect(sharedFileCard(.failed(.disconnected), machineOnline: false, sessionExited: false) == .offline)
        #expect(sharedFileCard(.failed(.disconnected), machineOnline: true, sessionExited: false) == .failed(.disconnected))
        #expect(sharedFileCard(.idle, machineOnline: true, sessionExited: false) == .available)
    }

    @Test func subtitlesAreTheSpecsInChinese() {
        zh {
            let size = Int64(24.3 * 1024 * 1024)
            let name = "daocheng-final.mp4"
            #expect(sharedFileSubtitle(.available, name: name, size: size) == "视频 · 24.3 MB")
            #expect(sharedFileSubtitle(.downloading(done: Int64(8.1 * 1024 * 1024), total: size, etaSeconds: nil), name: name, size: size) == "8.1 / 24.3 MB")
            #expect(sharedFileSubtitle(.downloading(done: Int64(8.1 * 1024 * 1024), total: size, etaSeconds: 12), name: name, size: size) == "8.1 / 24.3 MB · 约 12 秒")
            #expect(sharedFileSubtitle(.paused(done: Int64(8.1 * 1024 * 1024), total: size), name: name, size: size) == "已暂停 · 8.1 / 24.3 MB")
            #expect(sharedFileSubtitle(.downloaded(durationMs: 102_000), name: name, size: size) == "1:42 · 24.3 MB")
            #expect(sharedFileSubtitle(.downloaded(durationMs: nil), name: name, size: size) == "视频 · 24.3 MB")
            #expect(sharedFileSubtitle(.downloaded(durationMs: 5_000), name: "cover.png", size: size) == "图片 · 24.3 MB")
            #expect(sharedFileSubtitle(.failed(.disconnected), name: name, size: size) == "没下载下来，连接断了")
            #expect(sharedFileSubtitle(.failed(.checksum), name: name, size: size) == "没下载下来，文件校验不一致")
            #expect(sharedFileSubtitle(.failed(.diskFull), name: name, size: size) == "没下载下来，手机空间不足")
            #expect(sharedFileSubtitle(.failed(.other("disk quota")), name: name, size: size) == "没下载下来，disk quota")
            #expect(sharedFileSubtitle(.offline, name: name, size: size) == "电脑不在线")
            #expect(sharedFileSubtitle(.cleaned, name: name, size: size) == "会话已结束，文件已清理")
        }
    }

    @Test func subtitlesAreInEnglishToo() {
        en {
            let size = Int64(24.3 * 1024 * 1024)
            #expect(sharedFileSubtitle(.available, name: "a.zip", size: size) == "Archive · 24.3 MB")
            #expect(sharedFileSubtitle(.downloading(done: 1024 * 1024, total: size, etaSeconds: 3), name: "a.zip", size: size) == "1.0 / 24.3 MB · ~3 secs left")
            #expect(sharedFileSubtitle(.failed(.disconnected), name: "a.zip", size: size) == "Couldn't download, connection dropped")
        }
    }

    @Test func theControlFollowsTheState() {
        #expect(sharedFileControl(.available) == .download)
        #expect(sharedFileControl(.downloading(done: 0, total: 1, etaSeconds: nil)) == .pause)
        #expect(sharedFileControl(.paused(done: 1, total: 2)) == .resume)
        #expect(sharedFileControl(.downloaded(durationMs: nil)) == .open)
        #expect(sharedFileControl(.failed(.checksum)) == .retry)
        #expect(sharedFileControl(.offline) == .disabledDownload)
        #expect(sharedFileControl(.cleaned) == nil)
        zh {
            #expect(SharedFileControl.download.label == "下载")
            #expect(SharedFileControl.pause.label == "暂停")
            #expect(SharedFileControl.resume.label == "继续下载")
            #expect(SharedFileControl.open.label == "打开")
            #expect(SharedFileControl.retry.label == "重试")
        }
    }

    @Test func theMenuIsThatOfTheSpec() {
        #expect(sharedFileMenu(.available, kind: .video) == [.download])
        #expect(sharedFileMenu(.failed(.disconnected), kind: .video) == [.download])
        #expect(sharedFileMenu(.downloading(done: 1, total: 2, etaSeconds: nil), kind: .video) == [.pause, .cancelDownload])
        #expect(sharedFileMenu(.paused(done: 1, total: 2), kind: .video) == [.resume, .cancelDownload])
        #expect(sharedFileMenu(.downloaded(durationMs: nil), kind: .video) == [.saveToPhotos, .saveToFiles, .share, .deleteLocal])
        #expect(sharedFileMenu(.downloaded(durationMs: nil), kind: .image) == [.saveToPhotos, .saveToFiles, .share, .deleteLocal])
        #expect(sharedFileMenu(.downloaded(durationMs: nil), kind: .audio) == [.saveToFiles, .share, .deleteLocal])
        #expect(sharedFileMenu(.downloaded(durationMs: nil), kind: .pdf) == [.saveToFiles, .share, .deleteLocal])
        #expect(sharedFileMenu(.offline, kind: .video).isEmpty)
        #expect(sharedFileMenu(.cleaned, kind: .video).isEmpty)
        zh {
            #expect(SharedFileMenuItem.saveToFiles.label == "存到「文件」")
            #expect(SharedFileMenuItem.share.label == "分享…")
            #expect(SharedFileMenuItem.deleteLocal.label == "删除本机副本")
            #expect(SharedFileMenuItem.cancelDownload.label == "取消下载")
        }
        en { #expect(SharedFileMenuItem.saveToFiles.label == "Save to Files") }
    }

    @Test func voiceOverReadsNameKindSizeAndState() {
        let size = Int64(24.3 * 1024 * 1024)
        zh {
            #expect(sharedFileA11yLabel(.available, name: "daocheng-final.mp4", size: size) == "daocheng-final.mp4，视频，24.3 MB，可下载")
            #expect(sharedFileA11yLabel(.downloaded(durationMs: 1), name: "daocheng-final.mp4", size: size) == "daocheng-final.mp4，视频，24.3 MB，已下载")
            #expect(sharedFileA11yLabel(.offline, name: "a.zip", size: 10) == "a.zip，压缩包，10 B，电脑不在线")
        }
    }

    // MARK: Notification

    @Test func theNotificationNamesTheFilesAfterTheText() {
        zh {
            #expect(sharedFileNotificationBody(text: "改好了", files: 0) == "改好了")
            #expect(sharedFileNotificationBody(text: "改好了", files: 2) == "改好了 附 2 个文件")
            #expect(sharedFileNotificationBody(text: "", files: 1) == "附 1 个文件")
            var content = PBNotifyContent()
            content.sharedFileCount = 3
            #expect(content.turnEndBody(text: "完成") == "完成 附 3 个文件")
        }
        en {
            #expect(sharedFileNotificationBody(text: "Done", files: 1) == "Done 1 file attached")
            #expect(sharedFileNotificationBody(text: "", files: 2) == "2 files attached")
        }
    }

    // MARK: Rows

    private func shared(_ seq: UInt64, id: String = "f1", name: String = "a.mp4") -> PBEvent {
        Mk.ev(seq, .toolCall(.with {
            $0.title = "send_file"; $0.callID = "c\(seq)"; $0.status = "completed"
            $0.sharedFiles = [.with { $0.fileID = id; $0.name = name; $0.sizeBytes = 10; $0.sha256 = Data(count: 32); $0.path = "/Users/me/\(name)" }]
        }))
    }

    private func plain(_ seq: UInt64) -> PBEvent {
        Mk.ev(seq, .toolCall(.with { $0.title = "ls"; $0.callID = "p\(seq)"; $0.status = "completed" }))
    }

    private func rows(_ events: [PBEvent], supported: Bool) -> [ChatItem] {
        buildChat(events, d: derive(events, agent: .claude), machine: "Mac", sharedFiles: supported)
    }

    @Test func aCallWithFilesIsACardBetweenTheFoldedCallsAroundIt() {
        let events = [Mk.ev(1, Mk.turnStarted), plain(2), plain(3), shared(4), shared(5, id: "f2", name: "b.zip"), plain(6), plain(7)]
        let items = rows(events, supported: true)
        guard items.count == 4 else { Issue.record("\(items)"); return }
        if case .tools(let g) = items[0] { #expect(g.calls.count == 2) } else { Issue.record("first fold") }
        if case .sharedFiles(let s) = items[1] { #expect(s.files.map(\.fileID) == ["f1"] && s.seq == 4) } else { Issue.record("first card") }
        if case .sharedFiles(let s) = items[2] { #expect(s.files.map(\.fileID) == ["f2"]) } else { Issue.record("second card") }
        if case .tools(let g) = items[3] { #expect(g.calls.count == 2) } else { Issue.record("last fold") }
    }

    @Test func aComputerThatDoesNotSayItSharesFilesKeepsTheToolRow() {
        let items = rows([Mk.ev(1, Mk.turnStarted), shared(2)], supported: false)
        guard items.count == 1, case .tool(let t) = items[0] else { Issue.record("\(items)"); return }
        #expect(t.call.sharedFiles.count == 1)
    }

    @Test func aFailedSendIsAnOrdinaryFailedToolRow() {
        let failed = Mk.ev(2, .toolCall(.with { $0.title = "send_file"; $0.callID = "x"; $0.status = "failed" }))
        let items = rows([Mk.ev(1, Mk.turnStarted), failed], supported: true)
        guard items.count == 1, case .tool = items[0] else { Issue.record("\(items)"); return }
    }

    @Test func theProjectionBuildsAgainWhenTheCapabilityArrives() {
        var projection = ChatProjection()
        let events = [Mk.ev(1, Mk.turnStarted), shared(2)]
        let d = derive(events, agent: .claude)
        let before = projection.update(events, d: d, machine: "Mac", sharedFiles: false)
        let after = projection.update(events, d: d, machine: "Mac", sharedFiles: true)
        if case .tool = before[0] {} else { Issue.record("before") }
        if case .sharedFiles = after[0] {} else { Issue.record("after") }
    }
}
