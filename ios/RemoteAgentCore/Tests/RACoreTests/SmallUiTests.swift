import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app AccountCacheTest.
@Suite(.timeLimit(.minutes(2))) struct AccountCacheTests {
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
@Suite(.timeLimit(.minutes(2))) struct FollowTests {
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
