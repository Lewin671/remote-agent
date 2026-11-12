import RACore
import SwiftUI

/// 开源许可: the licences of what the app is built with (Android's
/// LicensesPage), each with its full text. No fonts are listed because none
/// is bundled: on Apple devices the design uses the system's own
/// (design-system §3).
struct LicensesScreen: View {
    @State private var open: License?

    var body: some View {
        let used = Licenses.all.filter(\.linked)
        let resolved = Licenses.all.filter { !$0.linked }
        SettingsPage {
            GroupLabel(L10n.settingsIosIconsAndLibraries())
            GroupCard {
                for license in used {
                    GRow(license.name, sub: license.use, value: license.kind, chevron: true) { open = license }
                }
            }
            GroupNote(L10n.settingsIosFontsUseTheSystemSSfProPingfangAnd())
            GroupLabel(L10n.settingsIosLibrariesResolvedWithDependencies())
            GroupCard {
                for license in resolved {
                    GRow(license.name, value: license.kind, chevron: true) { open = license }
                }
            }
            GroupNote(L10n.settingsIosTheseLibrariesAreUsedByOptionalPartsOfConnect())
        }
        .navBar(L10n.settingsIosOpenSourceLicenses())
        .kitSheet(item: $open, size: .large) { license in
            SheetHead(license.name, action: L10n.commonDone()) { open = nil }
            Text(Licenses.text(license).map(Licenses.display) ?? L10n.settingsIosLicenseTextIsNotBundledWithTheApp()).font(.footnote).ink(DS.ink2)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .onAppear {
            #if DEBUG
            if let id = SettingsDemo.state, open == nil { open = Licenses.all.first { $0.id == id } }
            #endif
        }
    }
}
