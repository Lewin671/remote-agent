// Words several screens share, from Android's ui/Common.kt, Manage.kt and ChatImages.kt.
import Foundation
import RAProto

/// Width over height of an image as the event says, so its place is kept before anything loads.
public func aspect(_ ref: PBImageRef) -> Double {
    let w: Int32, h: Int32
    if ref.previewWidth > 0 && ref.previewHeight > 0 {
        (w, h) = (ref.previewWidth, ref.previewHeight)
    } else if ref.displayWidth > 0 && ref.displayHeight > 0 {
        (w, h) = (ref.displayWidth, ref.displayHeight)
    } else if ref.width > 0 && ref.height > 0 {
        (w, h) = (ref.width, ref.height)
    } else {
        (w, h) = (4, 3)
    }
    return min(2.5, max(0.4, Double(w) / Double(h)))
}

/// A file's name from its path, for an image shown by name only.
public func fileName(_ path: String) -> String {
    let name = path.split(separator: "/", omittingEmptySubsequences: false).last.map(String.init) ?? ""
    return name.isEmpty ? L10n.imageTitle() : name
}
