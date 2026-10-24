import RACore
import SwiftUI

struct ScheduleTimingSheet: View {
    let onSave: (ScheduleTrigger) -> Void
    let onClose: () -> Void
    @State private var draft: ScheduleTiming
    @State private var error = ""
    @State private var zones = false
    @State private var search = ""
    init(trigger: ScheduleTrigger, onSave: @escaping (ScheduleTrigger) -> Void, onClose: @escaping () -> Void) {
        self.onSave = onSave; self.onClose = onClose
        _draft = State(initialValue: ScheduleTiming(trigger: trigger))
    }
    var body: some View {
        if zones {
            SheetHead("时区", onBack: { zones = false })
            InputField("时区", text: $search, placeholder: "搜索 IANA 时区")
            ForEach(TimeZone.knownTimeZoneIdentifiers.filter { search.isEmpty || $0.localizedCaseInsensitiveContains(search) }, id: \.self) { zone in
                ListItem(zone, checked: canonicalScheduleZone(zone) == canonicalScheduleZone(draft.timezone)) { draft.timezone = zone; zones = false }
            }
        } else {
            SheetHead("什么时候", action: "完成", onAction: save)
            SegPick(draft.kind == .afterTurn ? ["一次", "每天", "每周", "每隔", "本轮后"] : ["一次", "每天", "每周", "每隔"], selected: Binding(get: { draft.kind.rawValue }, set: { draft.kind = ScheduleTiming.Kind(rawValue: $0) ?? .daily }))
                .padding(.bottom, 12)
            if draft.kind == .once {
                DatePicker("时间", selection: $draft.date, in: Date()..., displayedComponents: [.date, .hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
            } else if draft.kind == .daily || draft.kind == .weekly {
                DatePicker("时间", selection: $draft.date, displayedComponents: [.hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
                if draft.kind == .daily { Toggle("只在工作日", isOn: $draft.workdays).padding(.vertical, 8) }
                else {
                    HStack(spacing: 4) {
                        ForEach(1...7, id: \.self) { day in
                            Button {
                                if draft.weekdays.contains(day) { draft.weekdays.remove(day) } else { draft.weekdays.insert(day) }
                            } label: {
                                Text(["一", "二", "三", "四", "五", "六", "日"][day - 1]).type(T.body, draft.weekdays.contains(day) ? Theme.blue : Theme.fg)
                                    .frame(maxWidth: .infinity, minHeight: 44).background(draft.weekdays.contains(day) ? Theme.soft : Theme.card, in: RoundedRectangle(cornerRadius: 10))
                            }.buttonStyle(PressStyle()).accessibilityLabel("星期" + ["一", "二", "三", "四", "五", "六", "日"][day - 1]).accessibilityAddTraits(draft.weekdays.contains(day) ? .isSelected : [])
                        }
                    }
                }
            } else {
                HStack {
                    TextField("间隔", value: $draft.interval, format: .number).keyboardType(.numberPad).textFieldStyle(.roundedBorder)
                    Picker("单位", selection: $draft.unit) {
                        Text("秒").tag(1); Text("分钟").tag(60); Text("小时").tag(3600); Text("天").tag(86400)
                    }.labelsHidden()
                }
                if draft.kind == .afterTurn { Toggle("重复", isOn: $draft.afterRepeat) }
            }
            GRow("时区", value: draft.timezone, chevron: true) { zones = true }
            if draft.kind != .once {
                FieldLabel("结束")
                SegPick(["不设", "次数", "日期"], selected: Binding(get: { draft.end.rawValue }, set: { draft.end = ScheduleTiming.End(rawValue: $0) ?? .never }))
                if draft.end == .count { Stepper("自动运行 \(draft.count) 次后", value: $draft.count, in: 1...100000).padding(.vertical, 12) }
                if draft.end == .date { DatePicker("结束时间", selection: $draft.until, in: Date()..., displayedComponents: [.date, .hourAndMinute]).padding(.vertical, 12) }
            }
            if !error.isEmpty { Hint(error, color: Theme.red) }
            Hint("立即运行一次不占自动次数及日历当天名额。")
            TextAction("取消", alignment: .center, action: onClose)
        }
    }
    private func save() {
        do { onSave(try draft.trigger()); onClose() } catch { self.error = errorText(error) }
    }
}
