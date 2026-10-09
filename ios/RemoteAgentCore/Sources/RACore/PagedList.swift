import Foundation
import os

/// Immutable append snapshots of a session's history. A Swift array would
/// copy every event on each append, since the snapshot published before
/// still shares its storage; this copies the page table and at most one
/// partial page. Old snapshots never change.
///
/// Elements are values without identity, so whether one snapshot continues
/// another is told by the pages: each page belongs to a line of appends, and
/// along a line a page only grows. A second append to the same partial page
/// starts a line of its own, which remembers where it left the first.
public struct PagedList<Element: Sendable>: RandomAccessCollection, Sendable {
    static var pageSize: Int { 256 }

    private final class Page: Sendable {
        let items: [Element]
        /// The line of appends this page is on.
        let line: Line

        init(_ items: [Element], line: Line) {
            self.items = items
            self.line = line
        }

        /// The first page of a line of its own.
        convenience init(_ items: [Element]) {
            self.init(items, line: Line(filled: items.count))
        }

        /// Whether this page starts with everything `before` holds.
        func continues(_ before: Page) -> Bool {
            if before === self { return true }
            var line = self.line
            var shared = items.count
            // Up the lines this one branched off, as far as each was shared.
            while line !== before.line {
                guard let fork = line.fork else { return false }
                shared = Swift.min(shared, fork.at)
                line = fork.from
            }
            return before.items.count <= shared
        }
    }

    /// One page as it grows from snapshot to snapshot. Only the longest
    /// page of a line may be extended on it.
    private final class Line: Sendable {
        /// The line this one left, and how many items they share.
        let fork: (from: Line, at: Int)?
        /// How many items the line's longest page holds.
        private let filled: OSAllocatedUnfairLock<Int>

        init(filled: Int, fork: (from: Line, at: Int)? = nil) {
            self.fork = fork
            self.filled = OSAllocatedUnfairLock(initialState: filled)
        }

        /// Whether a page of `from` items becomes one of `to` on this line: only when no other took that step.
        func extend(from: Int, to: Int) -> Bool {
            filled.withLock {
                if $0 != from { return false }
                $0 = to
                return true
            }
        }
    }

    private var pages: [Page] = []
    public private(set) var count = 0

    public init() {}

    public init(_ values: some Sequence<Element>) {
        self = PagedList().appending(Array(values))
    }

    public var startIndex: Int { 0 }
    public var endIndex: Int { count }

    public subscript(index: Int) -> Element {
        precondition(index >= 0 && index < count, "index \(index), size \(count)")
        return pages[index / Self.pageSize].items[index % Self.pageSize]
    }

    public func appending(_ values: [Element]) -> PagedList {
        if values.isEmpty { return self }
        var next = self
        var offset = 0
        if count % Self.pageSize != 0, let last = pages.last {
            let n = Swift.min(Self.pageSize - last.items.count, values.count)
            let items = last.items + values[..<n]
            let onLine = last.line.extend(from: last.items.count, to: items.count)
            let line = onLine ? last.line : Line(filled: items.count, fork: (last.line, last.items.count))
            next.pages[next.pages.count - 1] = Page(items, line: line)
            offset = n
        }
        while offset < values.count {
            let end = Swift.min(offset + Self.pageSize, values.count)
            next.pages.append(Page(Array(values[offset..<end])))
            offset = end
        }
        next.count = count + values.count
        return next
    }

    /// Whether this list is `previous` with more appended, in a comparison
    /// per page, never per event. A sibling snapshot with a replaced tail
    /// cannot masquerade as an append: its partial page is on another line.
    /// Lists built apart are never each other's continuation, whatever they hold.
    public func extends(_ previous: PagedList) -> Bool {
        if count < previous.count { return false }
        for (i, before) in previous.pages.enumerated() where !pages[i].continues(before) { return false }
        return true
    }
}

extension PagedList: ExpressibleByArrayLiteral {
    public init(arrayLiteral elements: Element...) {
        self.init(elements)
    }
}

extension PagedList: Equatable where Element: Equatable {
    public static func == (a: PagedList, b: PagedList) -> Bool {
        a.count == b.count && (a.extends(b) || a.elementsEqual(b))
    }
}

/// Whether `events` still starts with everything `previous` held, so that what was derived from it can be kept.
public func unchangedPrefix<T>(_ events: PagedList<T>, _ previous: PagedList<T>) -> Bool {
    events.extends(previous)
}
