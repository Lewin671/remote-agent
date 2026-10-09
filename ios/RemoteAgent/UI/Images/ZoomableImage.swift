// A picture that can be pinched, double-tapped and panned, and dragged down
// to close: a UIScrollView, since it already does all of zooming the way
// Photos does (rubber-banding, keeping the point under the fingers, the
// deceleration), and none of that is worth doing again in SwiftUI.
import SwiftUI
import UIKit

struct ZoomableImage: UIViewRepresentable {
    let image: UIImage
    /// Counts the asks to zoom in or back out that did not come from a finger on the picture (VoiceOver's action).
    var zoomToggles = 0
    /// Zooming and closing happen without their animations.
    let reduceMotion: Bool
    /// How far the picture is being dragged down to close, in points; 0 when it is let go without closing.
    let onDrag: (CGFloat) -> Void
    /// It was dragged far or fast enough: close.
    let onClose: () -> Void

    /// Android's viewer zooms from 1 to 6.
    static let maxZoom: CGFloat = 6
    /// What a double tap zooms to from the whole picture.
    static let tapZoom: CGFloat = 2.5

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> ZoomScrollView {
        let view = ZoomScrollView()
        view.delegate = context.coordinator
        view.minimumZoomScale = 1
        view.maximumZoomScale = Self.maxZoom
        view.showsVerticalScrollIndicator = false
        view.showsHorizontalScrollIndicator = false
        view.contentInsetAdjustmentBehavior = .never
        view.backgroundColor = .clear
        view.decelerationRate = .fast
        // The picture is described to VoiceOver by the page around it.
        view.isAccessibilityElement = false
        view.picture.isAccessibilityElement = false

        let twice = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.doubleTapped(_:)))
        twice.numberOfTapsRequired = 2
        view.addGestureRecognizer(twice)
        let down = UIPanGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.dragged(_:)))
        down.delegate = context.coordinator
        down.maximumNumberOfTouches = 1
        view.addGestureRecognizer(down)
        context.coordinator.down = down
        context.coordinator.view = view
        return view
    }

    func updateUIView(_ view: ZoomScrollView, context: Context) {
        context.coordinator.parent = self
        if context.coordinator.toggles != zoomToggles {
            context.coordinator.toggles = zoomToggles
            context.coordinator.toggleZoom()
        }
        // The preview gives way to the display copy: the same picture, sharper, so the zoom stays where it is.
        if view.picture.image !== image {
            let same = view.picture.image.map { abs($0.size.width / max($0.size.height, 1) - image.size.width / max(image.size.height, 1)) < 0.02 } ?? false
            view.picture.image = image
            if !same { view.setZoomScale(1, animated: false) }
            view.setNeedsLayout()
        }
    }

    @MainActor
    final class Coordinator: NSObject, UIScrollViewDelegate, UIGestureRecognizerDelegate {
        var parent: ZoomableImage?
        weak var view: ZoomScrollView?
        weak var down: UIPanGestureRecognizer?
        var toggles = 0

        func viewForZooming(in scrollView: UIScrollView) -> UIView? { (scrollView as? ZoomScrollView)?.picture }

        func scrollViewDidZoom(_ scrollView: UIScrollView) { (scrollView as? ZoomScrollView)?.centerPicture() }

        @objc func doubleTapped(_ tap: UITapGestureRecognizer) {
            guard let view else { return }
            let animated = parent?.reduceMotion != true
            if view.zoomScale > 1.01 {
                view.setZoomScale(1, animated: animated)
            } else {
                // Towards the point tapped, as Photos does.
                let at = tap.location(in: view.picture)
                let size = CGSize(width: view.bounds.width / ZoomableImage.tapZoom, height: view.bounds.height / ZoomableImage.tapZoom)
                view.zoom(to: CGRect(x: at.x - size.width / 2, y: at.y - size.height / 2, width: size.width, height: size.height), animated: animated)
            }
        }

        /// Toggles the zoom as a double tap in the middle would.
        func toggleZoom() {
            guard let view else { return }
            view.setZoomScale(view.zoomScale > 1.01 ? 1 : ZoomableImage.tapZoom, animated: parent?.reduceMotion != true)
        }

        // Dragging down closes, only from the whole picture: zoomed in, a drag pans.
        func gestureRecognizerShouldBegin(_ recognizer: UIGestureRecognizer) -> Bool {
            guard recognizer === down, let view, let pan = recognizer as? UIPanGestureRecognizer else { return true }
            let v = pan.velocity(in: view)
            return view.zoomScale <= 1.01 && v.y > 0 && v.y > abs(v.x) * 1.5
        }

        // The pager around this picture waits to see whether a drag is this one.
        func gestureRecognizer(_ recognizer: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
            recognizer === down && other is UIPanGestureRecognizer && other.view !== view
        }

        @objc func dragged(_ pan: UIPanGestureRecognizer) {
            guard let view else { return }
            let y = max(0, pan.translation(in: view).y)
            switch pan.state {
            case .changed:
                parent?.onDrag(y)
            case .ended:
                if y > 120 || pan.velocity(in: view).y > 900 { parent?.onClose() } else { parent?.onDrag(0) }
            case .cancelled, .failed:
                parent?.onDrag(0)
            default:
                break
            }
        }
    }
}

/// The scroll view and its picture: at zoom 1 the picture fits the view, whatever the view's size becomes.
final class ZoomScrollView: UIScrollView {
    let picture = UIImageView()
    private var fitted = CGSize.zero
    private var fittedFor: (bounds: CGSize, image: CGSize) = (.zero, .zero)

    override init(frame: CGRect) {
        super.init(frame: frame)
        picture.contentMode = .scaleAspectFit
        addSubview(picture)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not from a nib") }

    override func layoutSubviews() {
        super.layoutSubviews()
        let image = picture.image?.size ?? .zero
        // Fitted again when the view was turned or resized, or the picture is another one.
        if fittedFor.bounds != bounds.size || fittedFor.image != image {
            fittedFor = (bounds.size, image)
            let was = zoomScale
            zoomScale = 1
            if image.width > 0, image.height > 0, bounds.width > 0, bounds.height > 0 {
                let k = min(bounds.width / image.width, bounds.height / image.height)
                fitted = CGSize(width: image.width * k, height: image.height * k)
            } else {
                fitted = bounds.size
            }
            picture.frame = CGRect(origin: .zero, size: fitted)
            contentSize = fitted
            zoomScale = min(max(was, minimumZoomScale), maximumZoomScale)
        }
        centerPicture()
    }

    /// Keeps a picture smaller than the view in its middle.
    func centerPicture() {
        let x = max(0, (bounds.width - contentSize.width) / 2)
        let y = max(0, (bounds.height - contentSize.height) / 2)
        if contentInset.left != x || contentInset.top != y { contentInset = UIEdgeInsets(top: y, left: x, bottom: y, right: x) }
    }
}
