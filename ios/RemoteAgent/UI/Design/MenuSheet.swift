// A ⋯ menu as the system's bottom sheet (design-system §6 「弹层与对话框」):
// what it is about in bold at the top, then groups of rows in the
// system's inset grouped list, no centred buttons and no separate 取消.
// A setting reads 「名称 …… 值 ›」; danger is red; a row that cannot be
// chosen now is greyed with why under it; one that runs in place shows
// its progress, and its failure in red, and the sheet stays.
import SwiftUI

extension View {
    /// A ⋯ menu over this view: `groups` of rows, `info` at the top.
    func menuSheet<Info: View>(isPresented: Binding<Bool>, groups: [[SheetAction]], @ViewBuilder info: @escaping () -> Info) -> some View {
        sheet(isPresented: isPresented) {
            MenuSheet(groups: groups.filter { !$0.isEmpty }, info: info()) { isPresented.wrappedValue = false }
        }
    }
}

private struct MenuSheet<Info: View>: View {
    let groups: [[SheetAction]]
    let info: Info
    let dismiss: () -> Void

    var body: some View {
        // Leaving while a choice is running would drop its answer.
        let busy = groups.joined().contains { $0.busy }
        List {
            Section {
                info.listRowBackground(Color.clear).listRowInsets(EdgeInsets(top: 8, leading: 4, bottom: 0, trailing: 4))
            }
            ForEach(groups.indices, id: \.self) { g in
                Section {
                    ForEach(groups[g].indices, id: \.self) { i in row(groups[g][i]) }
                }
            }
        }
        .listStyle(.insetGrouped)
        .listSectionSpacing(12)
        .scrollBounceBehavior(.basedOnSize)
        .sheetSurface()
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(busy)
        .tint(DS.blue)
    }

    private func row(_ a: SheetAction) -> some View {
        let ink = a.value == nil && a.danger ? DS.red : a.value == nil && a.warn ? DS.orangeInk : DS.ink
        return Button {
            if a.danger && a.value == nil { Haptics.destructive() }
            if !a.keepOpen { dismiss() }
            a.action()
        } label: {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 8) {
                        if a.busy { ProgressView().controlSize(.small) }
                        Text(a.text).foregroundStyle(a.busy ? DS.ink2 : ink).opacity(a.enabled ? 1 : 0.4)
                    }
                    if !a.note.isEmpty {
                        Text(a.note).font(.footnote).foregroundStyle(a.noteError ? DS.red : DS.ink2)
                    }
                }
                // The name keeps its room; a long value (a model) is what gives way.
                .layoutPriority(1)
                Spacer(minLength: 0)
                if let value = a.value {
                    Text(value).foregroundStyle(a.danger ? DS.red : a.warn ? DS.orangeInk : DS.ink2).lineLimit(1)
                        .opacity(a.enabled ? 1 : 0.4)
                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3)
                }
            }
            .frame(minHeight: 30)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!a.enabled || a.busy)
        .listRowBackground(DS.raised)
    }
}

extension View {
    /// A bottom sheet's own ground (design-system §6 「弹层与对话框」): opaque
    /// `gbg`, never the system's glass that lets what is under it show
    /// through, top corners of 38, the grabber. A `List` in it keeps its
    /// rows on `DS.raised` and hides its own ground.
    func sheetSurface() -> some View {
        scrollContentBackground(.hidden)
            .background(DS.gbg)
            .presentationBackground(DS.gbg)
            .presentationCornerRadius(DS.Radius.sheet)
            .presentationDragIndicator(.visible)
    }
}
