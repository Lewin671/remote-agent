import Foundation
import SwiftUI
import Testing

@testable import RemoteAgent

@Suite struct ThemeTests {
    /// docs/design-system.md §2: text is at least 4.5:1 on its surface, in both themes.
    @Test(arguments: [("light", Palette.light), ("dark", Palette.dark)])
    func textMeetsTheContrastTheDesignAsksFor(name: String, p: Palette) {
        for surface in [p.bg, p.card, p.soft] {
            for text in [p.fg, p.fg2, p.muted] {
                #expect(text.contrast(with: surface) >= 4.5, "\(name)")
            }
        }
        // What is tapped, warned of or failed, on the surfaces it is drawn on.
        for surface in [p.card, p.soft] {
            for text in [p.blue, p.orangeInk, p.greenInk, p.red] {
                #expect(text.contrast(with: surface) >= 4.5, "\(name)")
            }
        }
        let white = RGBA(0xFFFFFF)
        #expect(white.contrast(with: p.blueBg) >= 4.5)
        #expect(white.contrast(with: p.redBg) >= 4.5)
    }

    @Test func contrastIsTheWcagRatio() {
        #expect(abs(RGBA(0x000000).contrast(with: RGBA(0xFFFFFF)) - 21) < 0.001)
        #expect(RGBA(0x777777).contrast(with: RGBA(0x777777)) == 1)
    }

    @Test func theScaleIsTheDesignSystems() {
        let sizes = [T.title30, T.h22, T.h17, T.body, T.bodyB, T.chat, T.sm, T.smB, T.meta, T.metaB, T.tab, T.mono, T.mono13].map(\.size)
        #expect(Set(sizes) == [30, 22, 17, 15, 13, 12, 11])
        #expect(T.bodyB.weight == .semibold)
        #expect(T.body.weight == .regular)
        #expect(Theme.Radius.xs == 6 && Theme.Radius.sm == 10 && Theme.Radius.md == 14 && Theme.Radius.lg == 20)
        #expect(Theme.minHit == 44)
    }

    @Test func avatarsPickTheSameColourAsTheOtherClients() {
        // h = h * 31 + code unit, kept to 32 bits, modulo 4: Android's avatarIndex and the web's.
        #expect(avatarIndex("foo") == 2)
        #expect(avatarIndex("") == 0)
        #expect(avatarIndex("remote-agent") == Int("remote-agent".utf16.reduce(UInt64(0)) { ($0 * 31 + UInt64($1)) & 0xffff_ffff } % 4))
        #expect(projectName("/Users/me/Code/foo") == "foo")
        #expect(projectName("/Users/me/Code/foo/") == "foo")
        #expect(projectName("/") == "未知项目")
    }
}

@Suite struct IconTests {
    @Test func everyIconParsesIntoItsGrid() {
        #expect(Lucide.all.count == 28)
        for icon in Lucide.all {
            let box = icon.path.boundingRect
            #expect(!icon.path.isEmpty, "\(icon.name)")
            // A 24 grid; the outlines stay a stroke's half inside it.
            #expect(box.minX >= 0.9 && box.minY >= 0.9 && box.maxX <= 23.1 && box.maxY <= 23.1, "\(icon.name) \(box)")
        }
        // Only the square that interrupts a turn is filled.
        #expect(Lucide.all.filter(\.filled).map(\.name) == ["square"])
    }

    @Test func pathDataIsReadAsSvgWritesIt() {
        #expect(SVGPath.parse("M5 12h14").boundingRect == CGRect(x: 5, y: 12, width: 14, height: 0))
        // Relative moves, and coordinates after a move that are lines.
        #expect(SVGPath.parse("m9 18 6-6-6-6").boundingRect == CGRect(x: 9, y: 6, width: 6, height: 12))
        // Numbers run together: "-.853.505" is two of them.
        #expect(SVGPath.parse("M10 10l-.853.505").boundingRect.width.isClose(to: 0.853))
        #expect(SVGPath.parse("M10 10l-.853.505").boundingRect.height.isClose(to: 0.505))
        // Commas, and H and V.
        #expect(SVGPath.parse("M52,54h14v4H52z").boundingRect == CGRect(x: 52, y: 54, width: 14, height: 4))
    }

    @Test func arcsAreDrawnAsTheCirclesTheyAre() {
        let circle = SVGPath.parse(SVGPath.circle(cx: 12, cy: 12, r: 3)).boundingRect
        #expect(circle.minX.isClose(to: 9) && circle.maxX.isClose(to: 15) && circle.minY.isClose(to: 9) && circle.maxY.isClose(to: 15))
        // Flags written without a separator before the next number: "0 1 1-6.219-8.56".
        let loader = SVGPath.parse("M21 12a9 9 0 1 1-6.219-8.56").boundingRect
        #expect(loader.minX.isClose(to: 3) && loader.maxX.isClose(to: 21) && loader.minY.isClose(to: 3) && loader.maxY.isClose(to: 21))
        // A quarter turn with a corner radius, as the rounded squares use.
        let corner = SVGPath.parse("M5 3a2 2 0 0 0-2 2").boundingRect
        #expect(corner.minX.isClose(to: 3) && corner.minY.isClose(to: 3) && corner.maxX.isClose(to: 5) && corner.maxY.isClose(to: 5))
    }

    @Test func dataThatStopsMakingSenseEndsThePathThere() {
        #expect(SVGPath.parse("").isEmpty)
        #expect(SVGPath.parse("M5 12h14 nonsense 3 4").boundingRect == CGRect(x: 5, y: 12, width: 14, height: 0))
    }

    @Test func anIconScalesIntoTheRectItIsGiven() {
        let box = Lucide.minus.path(in: CGRect(x: 0, y: 0, width: 48, height: 48)).boundingRect
        #expect(box == CGRect(x: 10, y: 24, width: 28, height: 0))
        let mark = AppMark().path(in: CGRect(x: 0, y: 0, width: 104, height: 94)).boundingRect
        #expect(mark.minX.isClose(to: 0) && mark.maxX.isClose(to: 104) && mark.minY.isClose(to: 0) && mark.maxY.isClose(to: 94))
    }
}

extension CGFloat {
    fileprivate func isClose(to other: CGFloat, within tolerance: CGFloat = 0.01) -> Bool { abs(self - other) <= tolerance }
}
