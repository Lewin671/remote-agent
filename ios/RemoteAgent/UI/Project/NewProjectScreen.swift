// 新项目 · 选目录 as a page (from the empty home page, and from 选项目 as
// a page): a computer, then a folder on it (Android's NewProjectScreen in
// ui/Project.kt).
import RACore
import SwiftUI

struct NewProjectScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ProjectDirPicker(model.state, pinned: true) { machine, path in
                    nav.open(.newProjectAt(machine: machine, path: path))
                }
            }
        }
        .navBar("新项目 · 选目录")
        .homeLaunchTweaks()
    }
}
