import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app AccountCacheTest.
@Suite struct AccountCacheTests {
    @Test func reusedImageIdsAndLateLoadsCannotCrossAccounts() {
        let cache = AccountCache<String>(limit: 6)
        let old = NSObject()
        let next = NSObject()
        cache.use(old)
        cache.put(old, key: "session/image", value: "private old image")
        #expect(cache.get(old, key: "session/image") == "private old image")
        cache.clear()
        cache.put(old, key: "session/image", value: "late old image")
        #expect(cache.get(old, key: "session/image") == nil)
        cache.use(next)
        cache.put(old, key: "session/image", value: "late old image")
        #expect(cache.get(next, key: "session/image") == nil)
        cache.put(next, key: "session/image", value: "new image")
        #expect(cache.get(next, key: "session/image") == "new image")
        #expect(cache.get(old, key: "session/image") == nil)
        cache.use(old)
        #expect(cache.get(old, key: "session/image") == nil)
    }

    @Test func cacheStillEvictsLeastRecentlyUsedImage() {
        let cache = AccountCache<String>(limit: 2)
        let owner = NSObject()
        cache.use(owner)
        cache.put(owner, key: "a", value: "a")
        cache.put(owner, key: "b", value: "b")
        _ = cache.get(owner, key: "a")
        cache.put(owner, key: "c", value: "c")
        #expect(cache.get(owner, key: "b") == nil)
        #expect(cache.get(owner, key: "a") == "a")
    }
}

/// Android's app FollowTest.
@Suite struct FollowTests {
    @Test func onlyTheUserStopsFollowing() {
        var f = Follow()
        // Opening a long history: the app's own jump ends before the list
        // reaches the bottom. That must not stop following.
        f.observe(userDragging: false, atBottom: false)
        #expect(f.on)
        f.observe(userDragging: true, atBottom: false)
        #expect(!f.on)
        // More messages arrive while scrolled up: still not following.
        f.observe(userDragging: false, atBottom: false)
        #expect(!f.on)
        f.observe(userDragging: false, atBottom: true)
        #expect(f.on)
    }
}

/// Words the screens share; Android has no tests of these.
@Suite struct CommonTests {
    @Test func theLinkAndThePreferencesAreSaidInAWord() {
        var st = RemoteState()
        #expect(connWord(st) == L10n.connectionConnectingConnCheck())
        #expect(prefsBlocked(st) == L10n.projectsConnectToEdit())
        st.link = .noServer
        #expect(connWord(st) == L10n.connectionUnreachable())
        st.synced = true
        #expect(connWord(st) == L10n.connectionReconnectingConnCheck())
        st.conn = .online
        #expect(connWord(st) == L10n.connectionConnected())
        #expect(prefsBlocked(st) == L10n.projectsLoading())
        st.prefs = .empty
        #expect(prefsBlocked(st) == nil)
        #expect(fileName("/tmp/a/shot.png") == "shot.png")
        #expect(fileName("/tmp/a/") == L10n.imageTitle())
        #expect(aspect(.with { $0.width = 400; $0.height = 100 }) == 2.5)
        #expect(aspect(.with { $0.previewWidth = 30; $0.previewHeight = 40; $0.width = 400; $0.height = 100 }) == 0.75)
        #expect(aspect(PBImageRef()) == 4.0 / 3.0)
        #expect(modeHelp(.full) == L10n.modesSkipAll())
    }
}

@Test func popupDiagnosticsNeverBecomeUserCopy() {
    for detail in ["trust folder", "hooks need review", "update available", "approval", "permission", "model menu", "model picker", "question", "plan", "permissions menu", "bypass warning", "menu", "artifacts", "unknown panel", "Native unrecognized panel"] {
        var session = SessionView(id: "popup-test", machineId: "machine")
        session.info = .with { $0.status = .blocked; $0.statusDetail = detail }
        #expect(previewOf(session, status: .popup) == L10n.listStatePopup())
    }
}
