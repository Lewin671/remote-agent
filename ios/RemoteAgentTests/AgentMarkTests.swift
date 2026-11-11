import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// The CLIs' marks (design-system §5 「CLI 的标」): their SVG data is read whole, and every mark lies in the box it is drawn on.
@Suite struct AgentMarkTests {
    private func bounds(_ d: String) -> CGRect? { SVGPathData.path(d)?.boundingRect }

    @Test func pathDataIsReadWithItsShorthand() throws {
        #expect(bounds("M1 2L4 2 4 6z") == CGRect(x: 1, y: 2, width: 3, height: 4))
        // Relative commands, h and v, a number run into the next (`.5.5`, `-1-1`) and a sign that starts one.
        #expect(bounds("m1 1h2v3H0V0") == CGRect(x: 0, y: 0, width: 3, height: 4))
        #expect(bounds("M0 0l.5.5-1-1") == CGRect(x: -0.5, y: -0.5, width: 1, height: 1))
        // Pairs after a moveto are lines; a curve takes its control points.
        #expect(bounds("M0 0 10 0 10 10") == CGRect(x: 0, y: 0, width: 10, height: 10))
        #expect(bounds("M0 0C0 10 10 10 10 0") != nil)
        // What it does not read, or what ends early, is nil rather than a half drawn mark.
        #expect(SVGPathData.path("M0 0Q1 1 2 2") == nil)
        #expect(SVGPathData.path("M0 0L1") == nil)
        #expect(SVGPathData.path("10 10") == nil)
    }

    @Test func anArcIsDrawnAsTheCircleItIs() throws {
        // Half a circle of radius 5 from (0,0) to (10,0), clockwise on the screen: it bulges up.
        let up = try #require(bounds("M0 0a5 5 0 0 1 10 0"))
        #expect(abs(up.minX) < 0.01 && abs(up.maxX - 10) < 0.01 && abs(up.minY + 5) < 0.05 && abs(up.maxY) < 0.01)
        // The other way, it bulges down; flags may run into the numbers after them.
        let down = try #require(bounds("M0 0a5 5 0 0010 0"))
        #expect(abs(down.minY) < 0.01 && abs(down.maxY - 5) < 0.05)
        // A radius too small for the chord grows to fit: half a circle on a chord of 10.
        let grown = try #require(bounds("M0 0a1 1 0 0 1 10 0"))
        #expect(abs(grown.minY + 5) < 0.05)
    }

    @Test func eachCLIHasAMarkThatFillsItsBox() throws {
        for agent in [PBAgent.codex, .claude, .antigravity] {
            let glyph = try #require(AgentGlyph(agent))
            let drawn = glyph.path.boundingRect
            #expect(!drawn.isEmpty)
            // Inside the box, and nearly all of it: a mark that stopped short would be a path read in part.
            let box = glyph.box.insetBy(dx: -0.5, dy: -0.5)
            #expect(box.contains(drawn), "\(agent)")
            #expect(drawn.width > glyph.box.width * 0.7 && drawn.height > glyph.box.height * 0.5, "\(agent)")
            // Fitted to a square it stays inside it, centred.
            let fitted = AgentMarkShape(glyph: glyph).path(in: CGRect(x: 0, y: 0, width: 20, height: 20)).boundingRect
            #expect(CGRect(x: -0.01, y: -0.01, width: 20.02, height: 20.02).contains(fitted), "\(agent)")
            #expect(abs(fitted.midX - 10) < 2 && abs(fitted.midY - 10) < 2, "\(agent)")
            #expect(glyph.image(side: 17).size == CGSize(width: 17, height: 17))
        }
        // A CLI without a mark draws none.
        #expect(AgentGlyph(.unspecified) == nil)
    }
}
