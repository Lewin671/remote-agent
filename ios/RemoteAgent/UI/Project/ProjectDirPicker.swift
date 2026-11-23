// Choosing a folder for a new project, rendered from `PBCoreDirPickerView`.
import RACore
import RAProto
import SwiftUI

/// Where a folder's listing comes from in sample mode: a made-up tree.
@MainActor
struct DirectorySource {
    var list: @MainActor (_ machine: String, _ path: String) async throws -> PBListDirectoriesResponse
}

/// Core-backed directory picker driven by `PBCoreDirPickerView`, isolated by `pickerKey`.
struct CoreDirPicker: View {
    let pickerKey: String
    var machine: String? = nil
    var onPage: Bool = false
    var forNewTask: Bool = false
    let onChosen: (_ machine: String, _ path: String) -> Void
    @Environment(AccountContext.self) private var context

    var body: some View {
        let picker = context.core.dirPickers[pickerKey] ?? PBCoreDirPickerView()
        CoreDirPickerContent(
            picker: picker,
            onPage: onPage,
            showMachineSwitch: machine == nil && picker.machines.count > 1,
            onSelectMachine: { next in
                context.dispatch {
                    $0.projectOpenDirPicker = .with {
                        $0.machineID = next
                        $0.path = ""
                        $0.forNewTask = forNewTask
                        $0.pickerKey = pickerKey
                    }
                }
            },
            onNavigate: { path in
                context.dispatch {
                    $0.projectDirNavigate = .with {
                        $0.machineID = picker.machineID
                        $0.path = path
                        $0.pickerKey = pickerKey
                    }
                }
            },
            onRetry: {
                context.dispatch {
                    $0.projectDirNavigate = .with {
                        $0.machineID = picker.machineID
                        $0.path = picker.currentPath
                        $0.pickerKey = pickerKey
                    }
                }
            },
            onConfirm: {
                Haptics.confirm()
                let chosenMachine = picker.machineID
                let chosenPath = picker.currentPath
                context.dispatch { $0.projectCloseDirPicker = .with { $0.pickerKey = pickerKey } }
                onChosen(chosenMachine, chosenPath)
            }
        )
        .task(id: "\(pickerKey)|\(machine ?? "")") {
            let token = await context.coreHost.acquireDirPicker(key: pickerKey)
            let initialPath = HomeLaunch.value("-dirPath") ?? ""
            context.dispatch {
                $0.projectOpenDirPicker = .with {
                    $0.machineID = machine ?? ""
                    $0.path = initialPath
                    $0.forNewTask = forNewTask
                    $0.pickerKey = pickerKey
                }
            }
            defer {
                let host = context.coreHost
                let key = pickerKey
                Task {
                    await host.dispatch(PBCoreEvent.with { $0.projectCloseDirPicker = .with { $0.pickerKey = key } })
                    await host.releaseDirPicker(token)
                }
            }
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3600))
            }
        }
        .debugSignal("dir.first") {
            if let first = picker.dirs.first {
                context.dispatch {
                    $0.projectDirNavigate = .with {
                        $0.machineID = picker.machineID
                        $0.path = first.path
                        $0.pickerKey = pickerKey
                    }
                }
            }
        }
        .debugSignal("dir.up") {
            if !picker.parentPath.isEmpty {
                context.dispatch {
                    $0.projectDirNavigate = .with {
                        $0.machineID = picker.machineID
                        $0.path = picker.parentPath
                        $0.pickerKey = pickerKey
                    }
                }
            }
        }
        .debugSignal("dir.choose") {
            if picker.canConfirm {
                let m = picker.machineID
                let p = picker.currentPath
                context.dispatch { $0.projectCloseDirPicker = .with { $0.pickerKey = pickerKey } }
                onChosen(m, p)
            }
        }
    }
}

/// Pure presentation body for `PBCoreDirPickerView`.
struct CoreDirPickerContent: View {
    let picker: PBCoreDirPickerView
    var onPage = false
    var showMachineSwitch = true
    var onSelectMachine: (String) -> Void = { _ in }
    var onNavigate: (String) -> Void = { _ in }
    var onRetry: () -> Void = {}
    var onConfirm: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if showMachineSwitch {
                MachineSegmented(
                    items: picker.machines.map { .init(id: $0.machineID, name: $0.name, online: $0.online) },
                    selected: Binding(
                        get: { picker.machineID },
                        set: { if $0 != picker.machineID { onSelectMachine($0) } }
                    )
                )
                .padding(.bottom, 10)
            }
            listing
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(.horizontal, onPage ? DS.Space.l : 0)
        .padding(.bottom, onPage ? 8 : 0)
        .readableColumn()
        .sheetPinned()
    }

    @ViewBuilder private var listing: some View {
        let errorText = picker.error.rendered()
        if picker.machineID.isEmpty {
            Banner(L10n.projectsIosNoOnline(), tone: .warn)
        } else if !errorText.isEmpty {
            Banner(errorText, tone: .error, action: L10n.commonRetry(), onAction: onRetry)
        } else if !picker.currentPath.isEmpty || !picker.dirs.isEmpty || !picker.loading {
            crumbs
            if picker.dirs.isEmpty && !picker.loading {
                Text(L10n.projectsNoSubdirectories()).font(.subheadline).ink(DS.ink2).padding(.vertical, 8)
            }
            ScrollView {
                VStack(spacing: 0) {
                    if !picker.parentPath.isEmpty {
                        ListItem(L10n.projectsParentDirectory(), last: picker.dirs.isEmpty, icon: "chevron.left") {
                            onNavigate(picker.parentPath)
                        }
                    }
                    ForEach(Array(picker.dirs.enumerated()), id: \.element.path) { idx, dir in
                        ListItem(dir.name, last: idx == picker.dirs.count - 1) {
                            onNavigate(dir.path)
                        }
                        .trailing { Arrow(DS.ink2, style: .body) }
                    }
                }
                .opacity(picker.loading ? 0.45 : 1)
                .disabled(picker.loading)
            }
            .scrollEdgeFades()
            .frame(maxHeight: .infinity, alignment: .top)
            Btn(picker.confirmLabel.rendered(), kind: .primary, enabled: picker.canConfirm, action: onConfirm)
                .padding(.top, 10)
        } else {
            LoadingRow(inset: 0)
        }
    }

    private var crumbs: some View {
        var line = Text("")
        for (i, crumb) in picker.crumbs.enumerated() {
            if i > 0 { line = line + Text(" / ") }
            let label = crumb.label.rendered()
            let isLast = i == picker.crumbs.count - 1
            line = line + (isLast ? Text(label).fontWeight(.semibold).foregroundStyle(DS.ink) : Text(label))
        }
        return line
            .font(.footnote.monospaced())
            .ink(DS.ink2)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, 6)
            .accessibilityLabel(L10n.projectsIosCurrentDirectory(path: picker.currentPath))
    }
}
