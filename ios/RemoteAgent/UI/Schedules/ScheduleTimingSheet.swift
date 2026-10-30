import RACore
import SwiftUI

struct ScheduleTimingSheet: View {
    let onSave: (ScheduleTrigger) -> Void
    let onClose: () -> Void
    private let original: ScheduleTrigger
    @State private var draft: ScheduleTiming
    @State private var error = ""
    @State private var zones = false
    @State private var search = ""
    init(trigger: ScheduleTrigger, onSave: @escaping (ScheduleTrigger) -> Void, onClose: @escaping () -> Void) {
        self.onSave = onSave; self.onClose = onClose; original = trigger
        var initial = ScheduleTiming(trigger: trigger)
        #if DEBUG
        if let kind = HomeLaunch.value("-schedulePanel").flatMap({ ["timing-once": ScheduleTiming.Kind.once, "timing-daily": .daily, "timing-weekly": .weekly, "timing-interval": .interval][$0] }) {
            initial.kind = kind
        }
        #endif
        _draft = State(initialValue: initial)
    }
    var body: some View {
        if zones {
            SheetHead("时区", onBack: { zones = false })
            InputField("时区", text: $search, placeholder: "搜索 IANA 时区")
            ForEach(TimeZone.knownTimeZoneIdentifiers.filter { search.isEmpty || $0.localizedCaseInsensitiveContains(search) }, id: \.self) { zone in
                ListItem(zone, checked: canonicalScheduleZone(zone) == canonicalScheduleZone(draft.timezone)) { draft.timezone = zone; zones = false }
            }
        } else {
            HStack {
                Text("什么时候").type(T.h17, Theme.fg); Spacer()
                TextAction("完成", enabled: draft.kind != .weekly || !draft.weekdays.isEmpty, action: save)
            }.padding(.bottom, 10)
            SegPick(draft.kind == .afterTurn ? ["一次", "每天", "每周", "每隔", "轮结束后"] : ["一次", "每天", "每周", "每隔"], selected: Binding(get: { draft.kind.rawValue }, set: { draft.kind = ScheduleTiming.Kind(rawValue: $0) ?? .daily }))
                .padding(.bottom, 12)
            if draft.kind == .once {
                // Validate on completion; a range would silently replace a retained past clock selection.
                DatePicker("日期和时间", selection: $draft.date, displayedComponents: [.date, .hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
                    .environment(\.locale, Locale(identifier: "zh_CN"))
            } else if draft.kind == .daily || draft.kind == .weekly {
                DatePicker("时间", selection: $draft.date, displayedComponents: [.hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
                    .environment(\.locale, Locale(identifier: "zh_CN"))
                if draft.kind == .daily { Toggle("只在工作日", isOn: $draft.workdays).padding(.vertical, 8) }
                else {
                    HStack(spacing: 4) {
                        ForEach(1...7, id: \.self) { day in
                            Button {
                                if draft.weekdays.contains(day) { draft.weekdays.remove(day) } else { draft.weekdays.insert(day) }
                            } label: {
                                Text("周" + ["一", "二", "三", "四", "五", "六", "日"][day - 1]).type(T.body, draft.weekdays.contains(day) ? Theme.fg : Theme.muted)
                                    .frame(maxWidth: .infinity, minHeight: 44).background(draft.weekdays.contains(day) ? Theme.card : Theme.soft, in: RoundedRectangle(cornerRadius: 10))
                            }.buttonStyle(PressStyle()).accessibilityLabel("星期" + ["一", "二", "三", "四", "五", "六", "日"][day - 1]).accessibilityAddTraits(draft.weekdays.contains(day) ? .isSelected : [])
                        }
                    }
                }
            } else if draft.kind == .interval {
                HStack {
                    TextField("间隔", value: $draft.interval, format: .number).keyboardType(.numberPad).textFieldStyle(.roundedBorder)
                    Picker("单位", selection: $draft.unit) {
                        Text("秒").tag(1); Text("分钟").tag(60); Text("小时").tag(3600); Text("天").tag(86400)
                    }.labelsHidden()
                }
                if draft.kind == .interval { Hint(scheduleIntervalHint) }
            } else {
                Text(scheduleFrequency(original) + "。选择上方其他频率可替换。").type(T.sm, Theme.fg)
            }
            if draft.kind != .once {
                FieldLabel("结束")
                SegPick(["不结束", "运行指定次数", "截止日期"], selected: Binding(get: { draft.end.rawValue }, set: { draft.end = ScheduleTiming.End(rawValue: $0) ?? .never }))
                if draft.end == .count { TextField("次数", value: $draft.count, format: .number).keyboardType(.numberPad).textFieldStyle(.roundedBorder).padding(.vertical, 12).accessibilityLabel("次数") }
                if draft.end == .date { DatePicker("截止时间", selection: $draft.until, displayedComponents: [.date, .hourAndMinute]).environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current).environment(\.locale, Locale(identifier: "zh_CN")).padding(.vertical, 12) }
            }
            if draft.kind != .interval { GRow("时区", value: draft.timezone, chevron: true) { zones = true } }
            if !error.isEmpty { Hint(error, color: Theme.red) }
            Hint("立即运行一次不占自动次数及日历当天名额。")
            TextAction("取消", alignment: .center, action: onClose)
        }
    }
    private func save() {
        do { onSave(try draft.trigger()); onClose() } catch { self.error = errorText(error) }
    }
}
