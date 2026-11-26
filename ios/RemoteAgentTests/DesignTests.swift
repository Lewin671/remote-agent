import enum RACore.L10n
import Foundation
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@Suite struct DesignTests {
    @Test func contrastIsTheWcagRatio() {
        #expect(abs(RGBA(0x000000).contrast(with: RGBA(0xFFFFFF)) - 21) < 0.001)
        #expect(RGBA(0x777777).contrast(with: RGBA(0x777777)) == 1)
    }

    @Test func aProjectIsNamedByItsLastFolder() {
        #expect(projectName("/Users/me/Code/foo") == "foo")
        #expect(projectName("/Users/me/Code/foo/") == "foo")
        #expect(projectName("/") == L10n.projectsUnknown())
    }

    @Test func textToBeReadKeepsItsContrastInBothThemes() {
        // design-system §2: ink and ink2 at least 4.5:1 on the page and on the grey surface, and the colours that are text on the page.
        for style in [ColorScheme.light, .dark] {
            var env = EnvironmentValues()
            env.colorScheme = style
            func rgba(_ c: Color) -> RGBA {
                var (r, g, b, a) = (CGFloat(0), CGFloat(0), CGFloat(0), CGFloat(0))
                UIColor(cgColor: c.resolve(in: env).cgColor).getRed(&r, green: &g, blue: &b, alpha: &a)
                var x = RGBA(0)
                (x.r, x.g, x.b, x.a) = (Double(r), Double(g), Double(b), Double(a))
                return x
            }
            for ground in [DS.paper, DS.gbg] {
                for text in [DS.ink, DS.ink2] {
                    #expect(rgba(text).contrast(with: rgba(ground)) >= 4.5, "\(style) \(text) on \(ground)")
                }
            }
            for text in [DS.blue, DS.orangeInk, DS.red] {
                #expect(rgba(text).contrast(with: rgba(DS.paper)) >= 4.5, "\(style) \(text)")
            }
            // The ink button's text on its ink.
            #expect(rgba(DS.btnFg).contrast(with: rgba(DS.btn)) >= 4.5, "\(style)")
        }
    }
}

@Suite struct AfterDismissTests {
    @Test func aRowThatClosesTheMenuActsOnceTheSheetHasGone() {
        var after = AfterDismiss()
        var ran = 0
        #expect(after.choose({ ran += 1 }, keepOpen: false) == nil)
        #expect(ran == 0)
        after.dismissed()?()
        #expect(ran == 1)
        // Swiping the sheet away later runs nothing again.
        #expect(after.dismissed() == nil)
    }

    @Test func aRowThatKeepsTheMenuOpenActsAtOnce() {
        var after = AfterDismiss()
        var ran = 0
        after.choose({ ran += 1 }, keepOpen: true)?()
        #expect(ran == 1)
        #expect(after.dismissed() == nil)
    }
}

@Suite struct ConnectionBannerPresentationTests {
    @Test func resolvesCoreConnBannerAndClockSkewText() {
        var conn = PBConnectionViewModel()
        #expect(ConnectionBannerPresentation.resolve(connection: conn) == nil)

        conn.banner = .with {
            $0.visible = true
            $0.kind = .noNetwork
            $0.text = .with { $0.key = "connection.no_network_empty" }
        }
        let noNet = ConnectionBannerPresentation.resolve(connection: conn)
        #expect(noNet?.symbol == "wifi.slash")
        #expect(noNet?.showDetails == false)
        #expect(noNet?.text == L10n.connectionNoNetworkEmpty())

        conn.banner = .with {
            $0.visible = true
            $0.kind = .noServer
            $0.errorTone = true
            $0.showDetails = true
            $0.text = .with { $0.key = "connection.server_retrying" }
            $0.detailText = .with { $0.literal = "Server error: 503" }
        }
        let errBanner = ConnectionBannerPresentation.resolve(connection: conn)
        #expect(errBanner?.tone == .error)
        #expect(errBanner?.symbol == "xmark")
        #expect(errBanner?.showDetails == true)
        #expect(errBanner?.text == "Server error: 503")

        conn.clearBanner()
        let skew = ConnectionBannerPresentation.resolve(
            connection: conn,
            clockSkewBannerText: L10n.errorsThisDeviceSClockIsBehindTheAccountState()
        )
        #expect(skew?.tone == .attention)
        #expect(skew?.symbol == "clock")
        #expect(skew?.text == L10n.errorsThisDeviceSClockIsBehindTheAccountState())
    }
}
