// The terminal's screen: SwiftTerm's view at the remote window's size, in
// an area that may be smaller or larger than it (architecture §11 「尺寸」;
// Android's AndroidView in term/TerminalPane.kt and its TerminalSession).
//
// SwiftTerm sizes its grid to its frame. Here the grid is the remote
// window's, so the view is made to be exactly that many cells, and shown
// at the largest size, from the one chosen down, at which the cells fit
// the area without the keyboard. The keyboard and a rotation therefore
// change neither the grid nor what is asked of the remote: with the
// keyboard up the area is shorter than the grid, and the view is moved up
// so that the cursor's line stays in sight, as Android scrolls its own.
//
// The size never goes below `terminalFontFloor` (6 pt), so a window wider
// than the area at that size keeps its 6 and is panned rather than shrunk
// further. The view sits in a horizontal panner (a scroll view): a
// sideways drag moves it, with the system's momentum and scroll bar, and
// nothing of it goes to the remote. The panner is only sideways — its
// content is no taller than the area — so the vertical pull that reads the
// record is left to the pane's own recognizer.
//
// The size shown is not always the emulator's font. SwiftTerm 1.11.2
// soft-resets the terminal whenever its font is set (`font` → `resetFont`
// → `TerminalView.resize` → `Terminal.softReset`): the program's
// cursor-key mode, its scroll region and the style it is writing in would
// go, and nothing from the remote puts them back. So the font is set only
// when a snapshot is about to replace the screen, to the size chosen; in
// between, a rotation or 字号 scales the view, and the view draws at that
// scale so the text stays sharp.
//
// What the remote's output may not do to the phone (android-client 「终端」):
//
//   - OSC 52 does not write the clipboard. SwiftTerm 1.11.2 hands it to
//     `TerminalViewDelegate.clipboardCopy`, which has no default on iOS;
//     the one here does nothing (`TerminalHostTests`). Copying what the
//     user selected is the view's own `copy(_:)` and still works;
//   - links are not opened: 1.11.2 never calls `requestOpenLink` on iOS,
//     and the one here does nothing;
//   - the bell is silent (the default would play a haptic), the title, the
//     current directory and OSC 777 notifications are ignored;
//   - OSC 1337 is taken before the emulator reads it: 1.11.2 decodes an
//     inline file (`File=inline=1:…`) and puts the image in the buffer
//     itself, without asking the view or `isProcessTrusted`. Its other
//     keys would only reach `iTermContent`, which does nothing here;
//   - device control strings and application program commands never reach
//     the emulator (`TerminalStringFilter`): 1.11.2 draws Sixel and Kitty
//     graphics from them the same way, and has no handler to replace;
//   - nothing the emulator itself says goes back as input: answers to
//     status and attribute queries, focus reports, mouse reports. tmux is
//     the terminal the program talks to and has answered already; a second
//     answer would arrive in the pane as typed text, and a query that
//     quotes its argument back would let output type on the user's behalf.
//     Only what the user types, pastes or taps on the key bar is sent.
import RACore
@preconcurrency import SwiftTerm
import UIKit

/// SwiftTerm's view with the emulator's own voice cut off.
final class RemoteTerminalView: TerminalView {
    override func send(source: Terminal, data: ArraySlice<UInt8>) {}
    override func bell(source: Terminal) {}
    override func setTerminalTitle(source: Terminal, title: String) {}
    override func setTerminalIconTitle(source: Terminal, title: String) {}
    override func windowCommand(source: Terminal, command: Terminal.WindowManipulationCommand) -> [UInt8]? { nil }
    override func isProcessTrusted(source: Terminal) -> Bool { false }
}

/// The panner: a scroll view that does not begin a pan while a text selection
/// is being dragged out, so the same drag does not also move the screen. The
/// scroll view is its own pan's delegate, so the decision is made here.
private final class PanScrollView: UIScrollView {
    var selectionActive: () -> Bool = { false }

    override func gestureRecognizerShouldBegin(_ gesture: UIGestureRecognizer) -> Bool {
        guard gesture === panGestureRecognizer, selectionActive() else { return super.gestureRecognizerShouldBegin(gesture) }
        return false
    }
}

final class TerminalHostView: UIView {
    /// Space kept free around the grid, in all: Android's 8 dp.
    private static let pad: CGFloat = 8
    /// The frame is this much more than its cells, so that dividing it by a cell gives the count and not one less.
    private static let slack: CGFloat = 0.25

    let terminal: RemoteTerminalView
    /// Holds the view when the window is wider than the area; scrolling it is the sideways pan.
    private let panner = PanScrollView()
    /// What the user typed, pasted or chose, to send to the remote.
    var onInput: ((Data) -> Void)?
    /// The columns and rows there is room for at the chosen size changed.
    var onCapacity: ((TerminalSize) -> Void)?
    /// The keyboard came up or went away.
    var onKeyboard: ((Bool) -> Void)?
    /// The screen was pulled down, by however many points so far: a wish to
    /// read the record, which the pane opens (`architecture §11 「回看历史」`).
    var onReadUp: ((CGFloat) -> Void)?

    /// The height of what the pane has under this view, down to the bottom inset: the key bar.
    var below: CGFloat = 0 {
        didSet { if below != oldValue { setNeedsLayout() } }
    }

    /// The size chosen with 字号, in points.
    var preferred: Int {
        didSet { if preferred != oldValue { refit() } }
    }

    /// The remote window's size; none before the first snapshot.
    private(set) var grid = TerminalSize(0, 0)
    private(set) var capacity = TerminalSize(0, 0)
    /// The emulator's font, in points: the size chosen when the last snapshot came.
    private var base: CGFloat
    /// The size shown, in pixels of a screen of `fontScale`, and what the view is scaled by to show it.
    private var pixels = 0
    private var fontScale: CGFloat = 1
    private var zoom: CGFloat = 1
    private var keyboardUp = false
    /// Takes Sixel and Kitty graphics out of the output, across frames.
    private var strings = TerminalStringFilter()
    /// How far the grid is moved up to keep the cursor above the keyboard.
    private var scrolled: CGFloat = 0
    /// The pane's pull down, kept so the panner and it may run together.
    private var pullGesture: UIPanGestureRecognizer?
    /// What the drag in hand is for, decided once by its dominant axis.
    private var dragAxis: TerminalDrag?

    init(preferred: Int = TerminalFont.preferred) {
        self.preferred = preferred
        let scale = max(1, UITraitCollection.current.displayScale)
        pixels = Int((CGFloat(preferred) * scale).rounded())
        fontScale = scale
        base = CGFloat(pixels) / scale
        terminal = RemoteTerminalView(frame: CGRect(x: 0, y: 0, width: 320, height: 240), font: TerminalFont.font(points: base))
        super.init(frame: .zero)
        // Placed by its top left corner, which a scale then leaves where it is.
        terminal.layer.anchorPoint = .zero
        terminal.center = .zero
        clipsToBounds = true
        backgroundColor = UIColor(Theme.Term.bg)

        terminal.terminalDelegate = self
        terminal.nativeBackgroundColor = UIColor(Theme.Term.bg)
        terminal.nativeForegroundColor = UIColor(Theme.Term.text)
        terminal.caretColor = UIColor(Theme.Term.text)
        terminal.selectedTextBackgroundColor = UIColor(Theme.Term.link).withAlphaComponent(0.35)
        terminal.backgroundColor = UIColor(Theme.Term.bg)
        // The key bar is the pane's own, and the keyboard is the terminal's colour in either theme.
        terminal.inputAccessoryView = nil
        terminal.keyboardAppearance = .dark
        terminal.contentInsetAdjustmentBehavior = .never
        // A tap selects or opens the menu whatever the program asked of the mouse; its reports are not sent anyway.
        terminal.allowMouseReporting = false
        let emulator = terminal.getTerminal()
        emulator.silentLog = true
        // The live screen keeps no scrollback of its own: with none the view
        // cannot scroll vertically, so the only way to what came before is the
        // reader. The emulator still writes its screen as tmux sized it.
        emulator.changeHistorySize(0)
        emulator.registerOscHandler(code: 1337) { _ in }
        terminal.isHidden = true
        // The view's own scrolling stays off: the panner around it is the only
        // scroll system, and sideways only.
        terminal.isScrollEnabled = false
        terminal.bounces = false
        // The panner holds the view and pans it sideways when the window is
        // wider than the area. Its content is no taller than the area, so it
        // never scrolls vertically and never competes with the pull down.
        panner.backgroundColor = .clear
        panner.contentInsetAdjustmentBehavior = .never
        panner.showsHorizontalScrollIndicator = true
        panner.indicatorStyle = .white
        panner.isDirectionalLockEnabled = true
        panner.alwaysBounceHorizontal = false
        // A tap is the keyboard's, at once, and not delayed for a scroll that may not come.
        panner.delaysContentTouches = false
        panner.selectionActive = { [weak self] in self?.terminal.selectionActive ?? false }
        addSubview(panner)
        panner.addSubview(terminal)
        // The pane's pull down: a drag whose dominant axis is vertical reads the
        // record; a sideways one is the panner's. The two run together.
        let pull = UIPanGestureRecognizer(target: self, action: #selector(pulled(_:)))
        // A trackpad or a mouse wheel comes to the same recognizer (iOS 13.4).
        pull.allowedScrollTypesMask = .all
        pull.delegate = self
        pullGesture = pull
        terminal.addGestureRecognizer(pull)

        isAccessibilityElement = true
        accessibilityLabel = "终端画面"
        accessibilityHint = "轻点两下调出键盘。方向键、回车和 Ctrl+C 在下面的按键栏"

        let center = NotificationCenter.default
        center.addObserver(self, selector: #selector(keyboardMoved(_:)), name: UIResponder.keyboardWillChangeFrameNotification, object: nil)
        center.addObserver(self, selector: #selector(keyboardGone(_:)), name: UIResponder.keyboardDidHideNotification, object: nil)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not from a nib") }

    // MARK: What the remote sends

    /// Replaces the screen with a snapshot of the remote terminal, at its size.
    func snapshot(cols: Int, rows: Int, data: Data) {
        grid = TerminalSize(max(4, cols), max(4, rows))
        terminal.isHidden = false
        resize()
        refit()
        // RIS: full reset and clear, as a fresh terminal would be.
        strings = TerminalStringFilter()
        terminal.feed(byteArray: ([0x1b, 0x63] + [UInt8](strings.take(data)))[...])
        terminal.contentOffset = CGPoint(x: 0, y: max(0, terminal.contentSize.height - terminal.bounds.height))
        followCursor()
    }

    /// Feeds output of the remote terminal.
    func append(_ data: Data) {
        guard grid.cols > 0 else { return }
        terminal.feed(byteArray: [UInt8](strings.take(data))[...])
        followCursor()
    }

    /// The program asked for application cursor keys.
    var appCursor: Bool { terminal.getTerminal().applicationCursor }

    // MARK: The keyboard

    var keyboardShown: Bool { terminal.isFirstResponder }

    func toggleKeyboard() {
        if terminal.isFirstResponder { _ = terminal.resignFirstResponder() } else { _ = terminal.becomeFirstResponder() }
    }

    func hideKeyboard() {
        if terminal.isFirstResponder { _ = terminal.resignFirstResponder() }
    }

    @objc private func keyboardMoved(_ note: Notification) {
        guard let end = (note.userInfo?[UIResponder.keyboardFrameEndUserInfoKey] as? NSValue)?.cgRectValue,
            let screen = window?.screen
        else { return }
        // A hardware keyboard leaves at most a bar at the bottom: that is not the keyboard being up.
        setKeyboard(end.minY < screen.bounds.height && end.height > 120)
    }

    @objc private func keyboardGone(_ note: Notification) {
        setKeyboard(false)
    }

    private func setKeyboard(_ up: Bool) {
        guard up != keyboardUp else { return }
        keyboardUp = up
        onKeyboard?(up)
        setNeedsLayout()
    }

    // MARK: Layout

    override func layoutSubviews() {
        super.layoutSubviews()
        panner.frame = bounds
        refit()
    }

    private var scale: CGFloat { max(1, window?.screen.scale ?? traitCollection.displayScale) }

    /// How much taller the area is without the keyboard. Worked out from
    /// where the area is in the window and not from the keyboard's
    /// notifications, which a view made while the keyboard is already up
    /// never gets: what is under the area is `below` and then the bottom
    /// inset, and anything more than that is the keyboard.
    private var covered: CGFloat {
        guard let window else { return 0 }
        let bottom = convert(CGPoint(x: 0, y: bounds.maxY), to: window).y + below
        let gap = window.bounds.height - window.safeAreaInsets.bottom - bottom
        return gap > 120 ? gap : 0
    }

    /// The size shown, in points.
    var shownPoints: CGFloat { CGFloat(pixels) / fontScale }

    /// The width the remote grid is shown at: the extent the live screen pans
    /// over, and the least the reader pans over too, so its offset lands the same.
    var gridWidth: CGFloat { CGFloat(grid.cols) * shownCell.width }

    /// The cell the live screen is drawing at, in points: the reader's column
    /// width and line height, so a line lands where it was.
    var shownCell: CGSize {
        let cell = TerminalFont.cell(points: base)
        return CGSize(width: cell.width * zoom, height: cell.height * zoom)
    }

    /// A pull down: the pane opens the reader. The dominant axis decides what
    /// the drag is — sideways is the panner's, downwards reads, upwards nothing
    /// — and a pull never sends anything: the remote is untouched.
    @objc private func pulled(_ gesture: UIPanGestureRecognizer) {
        switch gesture.state {
        case .began:
            // A new gesture, decided at the start: one that began sideways does not
            // turn into a downwards read because it curved before the next callback.
            dragAxis = nil
            classify(gesture)
        case .changed:
            // Only if the start was too short to say; then the first movement past
            // the threshold decides it, and it stays decided.
            classify(gesture)
            let translation = gesture.translation(in: self)
            guard dragAxis == .read, !terminal.selectionActive else { return }
            reportPull(translation.y)
        default:
            break
        }
    }

    /// Notes the direction of the drag from how far it has come, once it is long
    /// enough to say; a direction already noted stays.
    private func classify(_ gesture: UIPanGestureRecognizer) {
        guard dragAxis == nil else { return }
        let translation = gesture.translation(in: self)
        dragAxis = terminalDrag(dx: translation.x, dy: translation.y)
    }

    /// How far the screen has been pulled down, as the gesture reports it.
    func reportPull(_ distance: CGFloat) {
        onReadUp?(distance)
    }

    /// How far the live screen is panned sideways, and how far it can be. The
    /// picture is wider than the area exactly when there is room to pan.
    var horizontalOffset: CGFloat { panner.contentOffset.x }
    var maxHorizontalOffset: CGFloat { max(0, panner.contentSize.width - panner.bounds.width) }

    /// Pans the live screen sideways by `distance`, as a drag would (the demo's own signal).
    func pan(by distance: CGFloat) {
        let x = min(max(0, panner.contentOffset.x + distance), maxHorizontalOffset)
        panner.setContentOffset(CGPoint(x: x, y: 0), animated: false)
        panner.flashScrollIndicators()
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        // The window's screen may have another scale, and UIKit sets the view's own again.
        setNeedsLayout()
    }

    /// Makes the emulator the remote window's size at the size chosen. Only
    /// with a snapshot: both steps reset the terminal, which the snapshot
    /// does anyway.
    private func resize() {
        let wanted = (CGFloat(preferred) * scale).rounded() / scale
        if wanted != base {
            base = wanted
            // SwiftTerm reads the frame, which is the view's own size only while it is not scaled.
            zoom = 1
            terminal.transform = .identity
            // The frame first, at the new cell: changing the font works the grid out again from the frame.
            let cell = TerminalFont.cell(points: base)
            terminal.bounds.size = CGSize(
                width: CGFloat(grid.cols) * cell.width + Self.slack, height: CGFloat(grid.rows) * cell.height + Self.slack)
            terminal.font = TerminalFont.font(points: base)
        }
        let emulator = terminal.getTerminal()
        if emulator.cols != grid.cols || emulator.rows != grid.rows {
            terminal.resize(cols: grid.cols, rows: grid.rows)
        }
        // SwiftTerm's own measure of the cell decides the frame.
        let exact = terminal.getOptimalFrameSize().size
        let size = CGSize(width: exact.width + Self.slack, height: exact.height + Self.slack)
        if terminal.bounds.size != size { terminal.bounds.size = size }
    }

    /// Works out the size to show the grid at for the area there is now, and scales the view to it.
    private func refit() {
        guard bounds.width > 0, bounds.height > 0 else { return }
        // Opening the keyboard must neither shrink the font nor resize the shared window.
        let fullHeight = bounds.height + covered
        let scale = scale
        let width = Int(((bounds.width - Self.pad) * scale).rounded(.down))
        let height = Int(((fullHeight - Self.pad) * scale).rounded(.down))
        let wanted = Int((CGFloat(preferred) * scale).rounded())
        // Counted at the font the size chosen has once a snapshot comes, which is what a fit asks for.
        let room = TerminalFont.geometry(pixels: wanted, scale: scale, base: CGFloat(wanted) / scale).capacity(width: width, height: height)
        if room != capacity {
            capacity = room
            onCapacity?(room)
        }
        guard grid.cols > 0 else { return }
        let base = base
        // The floor is the smallest 字号 in points, counted here in the pixel
        // unit the search works in, so the shown size never drops below it.
        let floor = Int((CGFloat(terminalFontFloor) * scale).rounded())
        let size = terminalFontSize(preferred: wanted, smallest: floor, width: width, height: height, cols: grid.cols, rows: grid.rows) {
            TerminalFont.geometry(pixels: $0, scale: scale, base: base)
        }
        pixels = size
        fontScale = scale
        let zoom = CGFloat(size) / scale / base
        if zoom != self.zoom {
            self.zoom = zoom
            terminal.transform = zoom == 1 ? .identity : CGAffineTransform(scaleX: zoom, y: zoom)
        }
        // Drawn at the scale it is shown at, not drawn at the font's and then stretched.
        if terminal.contentScaleFactor != scale * zoom {
            terminal.contentScaleFactor = scale * zoom
            terminal.setNeedsDisplay()
        }
        // What the panner holds: the view at the size it is shown plus the margin,
        // so a window wider than the area can be panned over the whole of a line.
        let content = CGSize(width: terminal.bounds.width * zoom + Self.pad, height: bounds.height)
        if panner.contentSize != content { panner.contentSize = content }
        panner.alwaysBounceHorizontal = content.width > bounds.width
        if panner.contentOffset.x > maxHorizontalOffset { panner.contentOffset.x = maxHorizontalOffset }
        followCursor()
    }

    /// Keeps the cursor's line in the area: the grid is taller than it while the keyboard is up.
    private func followCursor() {
        guard grid.rows > 0 else { return }
        // The grid as it is shown: the view's own size, scaled.
        let shown = terminal.bounds.height * zoom
        let line = (shown - Self.slack * zoom) / CGFloat(grid.rows)
        let content = max(bounds.height, shown + Self.pad)
        let most = content - bounds.height
        let row = CGFloat(terminal.getTerminal().getCursorLocation().y)
        let bottom = (row + 3) * line
        if bottom > scrolled + bounds.height || row * line < scrolled {
            scrolled = bottom - bounds.height
        }
        scrolled = min(max(scrolled, 0), most)
        let origin = CGPoint(x: Self.pad / 2, y: (Self.pad / 2 - scrolled).rounded())
        if terminal.center != origin { terminal.center = origin }
    }

    // MARK: VoiceOver

    /// The lines on the screen, without the blank ones.
    var screenText: String {
        let emulator = terminal.getTerminal()
        return (0..<emulator.rows).compactMap { row -> String? in
            // The emulator keeps what is outside the basic plane, an emoji, in a table of its own.
            let line =
                emulator.getLine(row: row)?
                .translateToString(trimRight: true, skipNullCellsFollowingWide: true) { emulator.getCharacter(for: $0) }
                .trimmingCharacters(in: .whitespaces) ?? ""
            return line.isEmpty ? nil : line
        }.joined(separator: "\n")
    }

    override var accessibilityValue: String? {
        get { grid.cols > 0 ? screenText : nil }
        set {}
    }

    override func accessibilityActivate() -> Bool {
        terminal.becomeFirstResponder()
    }
}

extension TerminalHostView: @preconcurrency TerminalViewDelegate {
    func send(source: TerminalView, data: ArraySlice<UInt8>) {
        onInput?(Data(data))
    }

    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {}
    func setTerminalTitle(source: TerminalView, title: String) {}
    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    func scrolled(source: TerminalView, position: Double) {}
    func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {}
    func bell(source: TerminalView) {}
    func clipboardCopy(source: TerminalView, content: Data) {}
    func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
    func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}

extension TerminalHostView: UIGestureRecognizerDelegate {
    /// The sideways pan is the panner's, the pull down is the pane's: the two
    /// run together, and neither waits for the other to fail.
    func gestureRecognizer(_ gesture: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        guard let pull = pullGesture else { return false }
        return (gesture === pull && other === panner.panGestureRecognizer)
            || (gesture === panner.panGestureRecognizer && other === pull)
    }
}
