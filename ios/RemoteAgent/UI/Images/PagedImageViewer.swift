import RACore
import SwiftUI

/// Message and draft images share paging, dark chrome and closing gestures.
struct PagedImageViewer<Picture: View, Info: View>: View {
    let count: Int
    @Binding var selection: Int
    let empty: String
    let onClose: () -> Void
    let picture: (Int, Binding<CGFloat>) -> Picture
    let info: (Int) -> Info
    @State private var drag: CGFloat = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let fade = 1 - min(drag / 300, 1)
        let page = min(max(selection, 0), max(count - 1, 0))
        ZStack {
            Color.black.opacity(0.4 + 0.6 * fade).ignoresSafeArea()
            if count > 0 {
                TabView(selection: $selection) {
                    ForEach(0..<count, id: \.self) { i in
                        picture(i, Binding(get: { drag }, set: { y in
                            if y > 0 || reduceMotion { drag = y }
                            else { withAnimation(.easeOut(duration: 0.2)) { drag = 0 } }
                        })).tag(i)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .never))
                .ignoresSafeArea().offset(y: drag)
            } else {
                Text(empty).font(.body).ink(ViewerInk.text).multilineTextAlignment(.center).padding(24)
            }
        }
        .overlay(alignment: .top) {
            ZStack {
                if count > 0 {
                    Text("\(page + 1) / \(count)").font(.body).ink(ViewerInk.text)
                        .accessibilityLabel("第 \(page + 1) 张，共 \(count) 张")
                }
                HStack {
                    Button(action: onClose) { SymbolImage("xmark", size: 22, color: ViewerInk.text).hitTarget() }
                        .buttonStyle(PressStyle()).accessibilityLabel(L10n.commonClose())
                    Spacer()
                }
            }
            .dynamicTypeSize(...DynamicTypeSize.accessibility1).frame(height: DS.minHit).padding(.horizontal, 4).opacity(fade)
        }
        .overlay(alignment: .bottom) { if count > 0 { info(page).opacity(fade) } }
        .toolbar(.hidden, for: .navigationBar).statusBarHidden()
        .accessibilityAction(.escape, onClose)
        .onChange(of: count) { _, _ in if selection >= count { selection = max(count - 1, 0) } }
    }
}
