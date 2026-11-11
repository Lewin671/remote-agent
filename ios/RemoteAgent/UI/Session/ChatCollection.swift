// UIKit owns chat virtualization, measured cell sizes and scrolling. SwiftUI
// still draws each row and owns the dock/composer outside this collection.
import RACore
import SwiftUI
import UIKit

struct ChatCollectionItem: Identifiable {
    let id: String
    /// Message values let unchanged offscreen cells retain their measured size.
    let row: ChatRow?
    let content: AnyView

    init<V: View>(_ id: String, row: ChatRow? = nil, @ViewBuilder content: () -> V) {
        self.id = id
        self.row = row
        self.content = AnyView(content())
    }
}

struct ChatCollection: UIViewControllerRepresentable {
    let items: [ChatCollectionItem]
    let env: ChatEnv
    let tracker: ChatScrollTracker
    let ready: Bool
    let joins: Int
    let prepended: Int
    let count: Int
    let interaction: (Bool) -> Void

    func makeUIViewController(context: Context) -> ChatCollectionController {
        let controller = ChatCollectionController(tracker: tracker)
        tracker.controller = controller
        return controller
    }

    func updateUIViewController(_ controller: ChatCollectionController, context: Context) {
        controller.update(self, environment: context.environment)
    }

    static func dismantleUIViewController(_ controller: ChatCollectionController, coordinator: ()) {
        controller.stopObserving()
    }
}

/// A row's cell. A reused cell starts from nothing of an entrance that was cut short.
final class ChatCell: UICollectionViewCell {
    override func prepareForReuse() {
        super.prepareForReuse()
        contentView.layer.removeAllAnimations()
        contentView.alpha = 1
        contentView.transform = .identity
    }
}

/// How a row that has just come in at the end of the chat arrives (design-system §4 动效).
private enum Entrance {
    /// A bubble: fades in and rises 8 points, 200 ms.
    case rise
    /// A tool row, a line of a turn's time: fades in, 120 ms.
    case fade
}

@MainActor
final class ChatCollectionController: UIViewController, UICollectionViewDelegate {
    private let tracker: ChatScrollTracker
    private(set) var collection: ChatCollectionView!
    private var source: UICollectionViewDiffableDataSource<Int, String>!
    private var items: [String: ChatCollectionItem] = [:]
    private var environment = EnvironmentValues()
    private var env: ChatEnv?
    private var entities: [String: UInt64] = [:]
    private var visibleFrames: [String: CGRect] = [:]
    private var joins: Int?
    private var rowCount = 0
    private var opened = false
    private var updating = false
    private var interaction: (Bool) -> Void = { _ in }
    private let work = ChatScrollWork()
    private var destination: Destination?
    /// The rows of the last update, in order, and the entrances of the rows new in this one. An entrance is
    /// only kept for a moment: a row whose cell is made later, when the reader scrolls to it, plays nothing.
    private var order: [String] = []
    private var entering: [String: (Entrance, CFTimeInterval)] = [:]
    private var animators: [String: UIViewPropertyAnimator] = [:]
    private var waiting: [String: (Entrance, UIView)] = [:]

    private enum Destination { case end, row(String) }

    init(tracker: ChatScrollTracker) {
        self.tracker = tracker
        super.init(nibName: nil, bundle: nil)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not from a nib") }

    override func loadView() {
        var configuration = UICollectionLayoutListConfiguration(appearance: .plain)
        configuration.showsSeparators = false
        configuration.backgroundColor = .clear
        let layout = UICollectionViewCompositionalLayout { _, layoutEnvironment in
            let section = NSCollectionLayoutSection.list(using: configuration, layoutEnvironment: layoutEnvironment)
            let margin = max(0, (layoutEnvironment.container.effectiveContentSize.width - 760) / 2)
            section.contentInsets = NSDirectionalEdgeInsets(top: 0, leading: margin, bottom: 0, trailing: margin)
            return section
        }
        collection = ChatCollectionView(frame: .zero, collectionViewLayout: layout)
        collection.backgroundColor = .clear
        collection.contentInsetAdjustmentBehavior = .never
        collection.alwaysBounceVertical = true
        collection.keyboardDismissMode = .interactive
        collection.selfSizingInvalidation = .enabledIncludingConstraints
        collection.delegate = self
        collection.accessibilityIdentifier = "chat-list"
        collection.beforeLayout = { [weak self] in self?.anchor() }
        collection.afterLayout = { [weak self] anchor in self?.laidOut(anchor) }
        collection.register(ChatCell.self, forCellWithReuseIdentifier: "row")
        source = UICollectionViewDiffableDataSource<Int, String>(collectionView: collection) { [weak self] list, path, id in
            guard let self, let item = self.items[id] else { return nil }
            let cell = list.dequeueReusableCell(withReuseIdentifier: "row", for: path)
            let entity = self.entities[id] ?? ChatDiagnosticRing.shared.identity()
            self.entities[id] = entity
            cell.contentConfiguration = UIHostingConfiguration {
                item.content
                    .environment(\.self, self.environment)
                    .environment(\.chatDiagnosticEntity, entity)
                    .id(id)
            }.margins(.all, 0).minSize(height: 0)
            cell.backgroundConfiguration = .clear()
            self.enter(cell, id)
            return cell
        }
        view = collection
    }

    override func viewDidAppear(_ animated: Bool) {
        super.viewDidAppear(animated)
        schedule()
    }

    func update(_ input: ChatCollection, environment: EnvironmentValues) {
        loadViewIfNeeded()
        updating = true
        defer { updating = false; schedule() }
        interaction = input.interaction
        self.environment = environment
        let changedEnvironment = env != input.env
        env = input.env
        rowCount = input.count
        let old = items
        let ids = input.items.map(\.id)
        let new = Dictionary(uniqueKeysWithValues: input.items.map { ($0.id, $0) })
        let joined = joins != nil && joins != input.joins
        if joins != input.joins {
            ChatDiagnosticRing.shared.record(.joins, entity: tracker.diagnosticEntity, Double(input.joins))
        }
        joins = input.joins
        // Save a surviving visible row before applying the prepend. New rows
        // may also replace a folded row at the join, so total height is not an anchor.
        let place = joined && !tracker.scroll.following && !moving ? anchor(surviving: Set(ids)) : nil
        if joined { tracker.prepended(input.prepended) }
        if place != nil { tracker.keep() }
        planEntrances(ids: ids, new: new, joined: joined)
        items = new
        for id in visibleFrames.keys where new[id] == nil {
            ChatDiagnosticRing.shared.record(.rowHidden, entity: entities[id] ?? 0)
            visibleFrames[id] = nil
        }
        entities = entities.filter { new[$0.key] != nil }
        var snapshot = NSDiffableDataSourceSnapshot<Int, String>()
        snapshot.appendSections([0])
        snapshot.appendItems(ids)
        let visible = Set(collection.indexPathsForVisibleItems.compactMap { source.itemIdentifier(for: $0) })
        snapshot.reconfigureItems(ids.filter { id in
            guard let previous = old[id], let next = new[id] else { return false }
            // Refresh every visible host's full SwiftUI environment and action
            // closures. Unchanged offscreen messages retain their size cache;
            // the few auxiliary rows (pending, outgoing, banners) have no value
            // to compare, so they are always refreshed.
            return changedEnvironment || visible.contains(id) || next.row == nil || previous.row != next.row
        })
        source.apply(snapshot, animatingDifferences: false)
        collection.layoutIfNeeded()
        if let place {
            restore(place)
            tracker.kept()
        }
        if input.ready && !opened {
            opened = true
            if destination == nil && tracker.scroll.following { destination = .end }
        }
    }

    /// What the rows new at the end of the chat do as they come: bubbles rise in, tool rows fade in. A history
    /// shown for the first time, history joining in front and a long catch-up are not new rows.
    private func planEntrances(ids: [String], new: [String: ChatCollectionItem], joined: Bool) {
        defer { order = ids }
        guard opened, !joined else { return }
        let seen = Set(order)
        let added = ids.filter { !seen.contains($0) }
        let content = ids.filter { $0 != "chat-end" }
        guard !added.isEmpty, added.count <= 4, Array(content.suffix(added.count)) == added else { return }
        let now = CACurrentMediaTime()
        entering = entering.filter { now - $0.value.1 < 1 }
        for id in added {
            let kind: Entrance?
            if id.hasPrefix("out-") || id.hasPrefix("pending-") {
                kind = .rise
            } else if let item = new[id]?.row?.item {
                switch item {
                case .user(let user): kind = user.from != nil ? .rise : nil
                case .tool, .running, .tools, .note, .turnEnd, .generated: kind = .fade
                default: kind = nil
                }
            } else {
                kind = nil
            }
            if let kind { entering[id] = (kind, now) }
        }
    }

    /// A row whose cell is made now begins its entrance unseen (clear) and plays it once the list has moved to its
    /// end, so that the entrance is what the reader sees and is not over by the time the row is in sight.
    private func enter(_ cell: UICollectionViewCell, _ id: String) {
        guard let (kind, since) = entering.removeValue(forKey: id), CACurrentMediaTime() - since < 0.6 else { return }
        let view = cell.contentView
        view.alpha = 0
        if kind == .rise && !Motion.reduced { view.transform = CGAffineTransform(translationX: 0, y: Motion.bubbleRise) }
        waiting[id] = (kind, view)
        // The list may not move at all (the reader is above): the entrance then plays after a moment.
        Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(150))
            self?.playEntrances()
        }
    }

    private func playEntrances() {
        let now = waiting
        waiting.removeAll()
        for (id, (kind, view)) in now {
            let long = kind == .rise && !Motion.reduced
            let animator = Motion.curve(long ? Motion.base : Motion.fast)
            animator.addAnimations {
                view.alpha = 1
                view.transform = .identity
            }
            animator.addCompletion { [weak self] _ in self?.animators[id] = nil }
            animators[id] = animator
            animator.startAnimation()
        }
    }

    private var moving: Bool {
        tracker.dragging || collection.isTracking || collection.isDragging || collection.isDecelerating
    }

    /// A single offset adjustment after UIKit lays out an update. No geometry
    /// state is written into SwiftUI and there is no anchor/estimate retry loop.
    struct Anchor {
        let id: String
        let top: CGFloat
        let offset: CGFloat
    }

    private func anchor(surviving: Set<String>? = nil) -> Anchor? {
        guard source != nil else { return nil }
        return collection.indexPathsForVisibleItems.sorted().compactMap { index -> Anchor? in
            guard let id = source.itemIdentifier(for: index), surviving?.contains(id) != false,
                  let frame = collection.cellForItem(at: index)?.frame,
                  frame.maxY > collection.contentOffset.y else { return nil }
            return Anchor(id: id, top: frame.minY, offset: collection.contentOffset.y)
        }.first
    }

    private func restore(_ place: Anchor) {
        guard !moving, let index = source.indexPath(for: place.id),
              let frame = collection.layoutAttributesForItem(at: index)?.frame else { return }
        let delta = frame.minY - place.top
        guard abs(delta) > 0.5 else { return }
        let offset = max(0, place.offset + delta)
        ChatDiagnosticRing.shared.record(.keepTarget, entity: entities[place.id] ?? 0, Double(place.top - place.offset), Double(delta))
        ChatDiagnosticRing.shared.record(.positionY, entity: tracker.diagnosticEntity, Double(offset))
        collection.contentOffset.y = offset
    }

    private func laidOut(_ place: Anchor?) {
        guard !updating else { return }
        // A cell above the first visible row can resize independently (for
        // example an image or an expanded block). Keep that row's screen Y.
        if !tracker.scroll.following, !moving, let place { restore(place) }
        schedule()
    }

    private func schedule() {
        work.schedule { [weak self] in self?.settle() }
    }

    @discardableResult
    private func observe() -> Bool {
        let height = collection.contentSize.height
        let offset = collection.contentOffset.y
        let floor = collection.bounds.height
        let ring = ChatDiagnosticRing.shared
        ring.record(.sight, entity: tracker.diagnosticEntity, Double(offset), Double(height), Double(floor))
        // These share UIKit viewport coordinates, independent of the window,
        // composer safe-area inset and interactive keyboard lag.
        let changed = tracker.viewport(height: height, offset: offset, floor: floor, count: rowCount)
        recordRows()
        return changed
    }

    private func settle() {
        guard collection.window != nil else { return }
        guard !moving else { observe(); return }
        if let destination {
            self.destination = nil
            switch destination {
            case .end: scrollToEnd()
            case .row(let id): scrollToRow(id)
            }
            observe()
        } else if observe() {
            tracker.follow { [weak self] in self?.scrollToEnd() }
        }
    }

    private func recordRows() {
        var frames: [String: CGRect] = [:]
        for index in collection.indexPathsForVisibleItems {
            guard let id = source.itemIdentifier(for: index), let entity = entities[id],
                  let attributes = collection.layoutAttributesForItem(at: index) else { continue }
            let frame = attributes.frame.offsetBy(dx: -collection.contentOffset.x, dy: -collection.contentOffset.y)
            guard frame.maxY > 0 && frame.minY < collection.bounds.height else { continue }
            frames[id] = frame
            if visibleFrames[id] != frame {
                ChatDiagnosticRing.shared.record(.row, entity: entity, Double(frame.minY), Double(frame.height), Double(frame.width), Double(tracker.diagnosticEntity))
            }
        }
        for id in visibleFrames.keys where frames[id] == nil {
            ChatDiagnosticRing.shared.record(.rowHidden, entity: entities[id] ?? 0)
        }
        visibleFrames = frames
    }

    func toEnd() { destination = .end; schedule() }
    func toRow(_ id: String) { destination = .row(id); schedule() }

    private func scrollToEnd() {
        guard !moving, let last = source.snapshot().itemIdentifiers.last, let index = source.indexPath(for: last) else { return }
        ChatDiagnosticRing.shared.record(.proxyEnd, entity: tracker.diagnosticEntity)
        collection.scrollToItem(at: index, at: .bottom, animated: false)
        collection.layoutIfNeeded()
        collection.contentOffset.y = max(0, collection.contentSize.height - collection.bounds.height)
        playEntrances()
    }

    private func scrollToRow(_ id: String) {
        guard !moving, let index = source.indexPath(for: id) else { return }
        tracker.leave()
        ChatDiagnosticRing.shared.record(.positionID, entity: entities[id] ?? 0, 0)
        collection.scrollToItem(at: index, at: .top, animated: false)
        collection.layoutIfNeeded()
        if let frame = collection.layoutAttributesForItem(at: index)?.frame {
            collection.contentOffset.y = min(frame.minY, max(0, collection.contentSize.height - collection.bounds.height))
        }
    }

    func scrollViewDidScroll(_ scrollView: UIScrollView) { schedule() }
    func scrollViewWillBeginDragging(_ scrollView: UIScrollView) {
        destination = nil
        work.cancel()
        _ = tracker.interaction(true)
        interaction(true)
    }
    func scrollViewDidEndDragging(_ scrollView: UIScrollView, willDecelerate decelerate: Bool) {
        if !decelerate { endedInteraction() }
    }
    func scrollViewDidEndDecelerating(_ scrollView: UIScrollView) { endedInteraction() }
    func scrollViewShouldScrollToTop(_ scrollView: UIScrollView) -> Bool {
        scrollViewWillBeginDragging(scrollView)
        return true
    }
    func scrollViewDidScrollToTop(_ scrollView: UIScrollView) { endedInteraction() }
    private func endedInteraction() {
        // Even a flick completed between two coalesced samples must be judged
        // with user ownership, before dropping the dragging/decelerating flag.
        observe()
        if tracker.interaction(false) { tracker.follow { [weak self] in self?.scrollToEnd() } }
        interaction(false)
        schedule()
    }

    func stopObserving() {
        animators.values.forEach { $0.stopAnimation(true) }
        animators.removeAll()
        waiting.values.forEach { $0.1.alpha = 1; $0.1.transform = .identity }
        waiting.removeAll()
        work.cancel()
        tracker.cancelFollow()
        _ = tracker.interaction(false)
        for id in visibleFrames.keys {
            ChatDiagnosticRing.shared.record(.rowHidden, entity: entities[id] ?? 0)
        }
        visibleFrames.removeAll()
        collection.delegate = nil
        collection.beforeLayout = nil
        collection.afterLayout = nil
        interaction(false)
    }
}

/// Scope diagnostics around our own UIKit view, without touching private hosts.
final class ChatCollectionView: UICollectionView {
    var beforeLayout: (() -> ChatCollectionController.Anchor?)?
    var afterLayout: ((ChatCollectionController.Anchor?) -> Void)?
    private var layingOut = false

    override func layoutSubviews() {
        let pass = ChatDiagnosticRing.shared.beginLayout(.listLayout)
        defer { ChatDiagnosticRing.shared.endLayout(pass) }
        guard !layingOut else { super.layoutSubviews(); return }
        layingOut = true
        defer { layingOut = false }
        let place = beforeLayout?()
        super.layoutSubviews()
        afterLayout?(place)
    }
}
