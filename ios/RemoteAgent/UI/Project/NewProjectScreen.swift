// 新项目 · 选目录 as a pushed page.
import RACore
import SwiftUI

struct NewProjectScreen: View {
    @Environment(Navigator.self) private var nav

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                CoreDirPicker(onPage: true) { machine, path in
                    nav.open(.newProjectAt(machine: machine, path: path))
                }
            }
        }
        .navBar(L10n.projectsNewChooseDirectory())
        .homeLaunchTweaks()
    }
}
