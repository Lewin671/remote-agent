// Draws the app icon and writes every PNG the web and iOS clients ship.
// Android draws the same mark as vectors (android/app/src/main/res/drawable);
// the geometry and colours are described in docs/design-system.md §5.
//
//   swift scripts/gen-icons.swift
import AppKit

struct Variant {
    let path: String
    let size: Int
    /// Mark size relative to the master drawing.
    var mark: CGFloat = 1
    /// Rounded corners on a transparent canvas, with a hairline of this
    /// width (in 512ths) so the light tile holds its edge on a light page.
    var hairline: CGFloat? = nil
}

let variants = [
    Variant(path: "web/public/favicon.png", size: 64, mark: 1.3, hairline: 14),
    Variant(path: "web/public/icon-192.png", size: 192, hairline: 4),
    Variant(path: "web/public/icon-512.png", size: 512, hairline: 4),
    Variant(path: "web/public/icon-maskable-512.png", size: 512, mark: 0.9),
    Variant(path: "web/public/apple-touch-icon.png", size: 180),
    Variant(path: "ios/RemoteAgent/Assets.xcassets/AppIcon.appiconset/AppIcon.png", size: 1024),
]

func rgb(_ hex: UInt32, _ alpha: CGFloat = 1) -> CGColor {
    CGColor(
        srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
        blue: CGFloat(hex & 0xFF) / 255, alpha: alpha)
}

func draw(_ v: Variant) throws {
    let space = CGColorSpace(name: CGColorSpace.sRGB)!
    let alpha: CGImageAlphaInfo = v.hairline == nil ? .noneSkipLast : .premultipliedLast
    let ctx = CGContext(
        data: nil, width: v.size, height: v.size, bitsPerComponent: 8, bytesPerRow: 0,
        space: space, bitmapInfo: alpha.rawValue)!
    // The master drawing is 512 wide with y pointing down.
    let k = CGFloat(v.size) / 512
    ctx.translateBy(x: 0, y: CGFloat(v.size))
    ctx.scaleBy(x: k, y: -k)

    let canvas = CGRect(x: 0, y: 0, width: 512, height: 512)
    let tile = CGPath(roundedRect: canvas, cornerWidth: 115, cornerHeight: 115, transform: nil)
    if v.hairline != nil {
        ctx.addPath(tile)
        ctx.clip()
    }
    let paper = CGGradient(colorsSpace: space, colors: [rgb(0xFFFFFF), rgb(0xE9EBF0)] as CFArray, locations: [0, 1])!
    ctx.drawLinearGradient(paper, start: .zero, end: CGPoint(x: 0, y: 512), options: [])
    if let width = v.hairline {
        // Clipped to the tile, so only the inner half of the stroke shows.
        ctx.addPath(tile)
        ctx.setStrokeColor(rgb(0x000000, 0.16))
        ctx.setLineWidth(width * 2)
        ctx.strokePath()
    }

    ctx.translateBy(x: 256, y: 256)
    ctx.scaleBy(x: v.mark, y: v.mark)
    ctx.translateBy(x: -256, y: -256)
    // The prompt.
    ctx.move(to: CGPoint(x: 146, y: 176))
    ctx.addLine(to: CGPoint(x: 238, y: 256))
    ctx.addLine(to: CGPoint(x: 146, y: 336))
    ctx.setStrokeColor(rgb(0x1D1D1F))
    ctx.setLineWidth(42)
    ctx.setLineCap(.round)
    ctx.setLineJoin(.round)
    ctx.strokePath()
    // The cursor, in the colour the clients use for "needs you".
    ctx.addPath(CGPath(roundedRect: CGRect(x: 276, y: 314, width: 110, height: 42), cornerWidth: 21, cornerHeight: 21, transform: nil))
    ctx.setFillColor(rgb(0xF5820A))
    ctx.fillPath()

    let rep = NSBitmapImageRep(cgImage: ctx.makeImage()!)
    try rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: v.path))
    print("\(v.path)  \(v.size)px")
}

let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
FileManager.default.changeCurrentDirectoryPath(root.path)
for v in variants { try draw(v) }
