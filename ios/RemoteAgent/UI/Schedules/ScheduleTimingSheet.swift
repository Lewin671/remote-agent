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
            SheetHead(L10n.scheduleTimeZone(), onBack: { zones = false })
            InputField(L10n.scheduleTimeZone(), text: $search, placeholder: L10n.scheduleIosSearchIanaTimeZones())
            ForEach(TimeZone.knownTimeZoneIdentifiers.filter { search.isEmpty || $0.localizedCaseInsensitiveContains(search) }, id: \.self) { zone in
                ListItem(zone, checked: canonicalScheduleZone(zone) == canonicalScheduleZone(draft.timezone)) { draft.timezone = zone; zones = false }
            }
        } else {
            HStack {
                Text(L10n.scheduleWhen()).font(.headline).ink(DS.ink); Spacer()
                TextAction(L10n.commonDone(), enabled: draft.kind != .weekly || !draft.weekdays.isEmpty, action: save)
            }.padding(.bottom, 10)
            SegPick(draft.kind == .afterTurn ? [L10n.scheduleOnceSchedules(), L10n.scheduleDaily(), L10n.scheduleWeeklyOn(), L10n.scheduleEvery(), L10n.scheduleAfterTurnEnds()] : [L10n.scheduleOnceSchedules(), L10n.scheduleDaily(), L10n.scheduleWeeklyOn(), L10n.scheduleEvery()], selected: Binding(get: { draft.kind.rawValue }, set: { draft.kind = ScheduleTiming.Kind(rawValue: $0) ?? .daily }))
                .padding(.bottom, 12)
            if draft.kind == .once {
                // Validate on completion; a range would silently replace a retained past clock selection.
                DatePicker(L10n.scheduleDateAndTime(), selection: $draft.date, displayedComponents: [.date, .hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
                    .environment(\.locale, Locale(identifier: MessageLocale.appleTags[MessageLocale.current]!))
            } else if draft.kind == .daily || draft.kind == .weekly {
                DatePicker(L10n.scheduleTime(), selection: $draft.date, displayedComponents: [.hourAndMinute])
                    .environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current)
                    .environment(\.locale, Locale(identifier: MessageLocale.appleTags[MessageLocale.current]!))
                if draft.kind == .daily { Toggle(L10n.scheduleWeekdaysOnly(), isOn: $draft.workdays).padding(.vertical, 8) }
                else {
                    HStack(spacing: 4) {
                        ForEach(1...7, id: \.self) { day in
                            Button {
                                if draft.weekdays.contains(day) { draft.weekdays.remove(day) } else { draft.weekdays.insert(day) }
                            } label: {
                                Text([L10n.timeWeekday(day: "mon"), L10n.timeWeekday(day: "tue"), L10n.timeWeekday(day: "wed"), L10n.timeWeekday(day: "thu"), L10n.timeWeekday(day: "fri"), L10n.timeWeekday(day: "sat"), L10n.timeWeekday(day: "sun")][day - 1]).font(.body).ink(draft.weekdays.contains(day) ? DS.ink : DS.ink2)
                                    .frame(maxWidth: .infinity, minHeight: 44).background(draft.weekdays.contains(day) ? DS.gbg : DS.gbg, in: RoundedRectangle(cornerRadius: 10))
                            }.buttonStyle(PressStyle()).accessibilityLabel([L10n.scheduleIosMonday(), L10n.scheduleIosTuesday(), L10n.scheduleIosWednesday(), L10n.scheduleIosThursday(), L10n.scheduleIosFriday(), L10n.scheduleIosSaturday(), L10n.scheduleIosSunday()][day - 1]).accessibilityAddTraits(draft.weekdays.contains(day) ? .isSelected : [])
                        }
                    }
                }
            } else if draft.kind == .interval {
                HStack {
                    TextField(L10n.scheduleIosInterval(), value: $draft.interval, format: .number).keyboardType(.numberPad).textFieldStyle(.roundedBorder)
                    Picker(L10n.scheduleIosUnit(), selection: $draft.unit) {
                        Text(L10n.scheduleIosSeconds()).tag(1); Text(L10n.scheduleIosMinutes()).tag(60); Text(L10n.scheduleIosHours()).tag(3600); Text(L10n.scheduleIosDays()).tag(86400)
                    }.labelsHidden()
                }
                if draft.kind == .interval { Hint(scheduleIntervalHint) }
            } else {
                Text(L10n.scheduleAfterTurnReplace(frequency: String(describing: scheduleFrequency(original)))).font(.subheadline).ink(DS.ink)
            }
            if draft.kind != .once {
                FieldLabel(L10n.sessionEnd())
                SegPick([L10n.scheduleNever(), L10n.scheduleAfterARunCount(), L10n.scheduleEndDate()], selected: Binding(get: { draft.end.rawValue }, set: { draft.end = ScheduleTiming.End(rawValue: $0) ?? .never }))
                if draft.end == .count { TextField(L10n.scheduleCount(), value: $draft.count, format: .number).keyboardType(.numberPad).textFieldStyle(.roundedBorder).padding(.vertical, 12).accessibilityLabel(L10n.scheduleCount()) }
                if draft.end == .date { DatePicker(L10n.scheduleEndTime(), selection: $draft.until, displayedComponents: [.date, .hourAndMinute]).environment(\.timeZone, TimeZone(identifier: draft.timezone) ?? .current).environment(\.locale, Locale(identifier: MessageLocale.appleTags[MessageLocale.current]!)).padding(.vertical, 12) }
            }
            if draft.kind != .interval { GRow(L10n.scheduleTimeZone(), value: draft.timezone, chevron: true) { zones = true } }
            if !error.isEmpty { Hint(error, color: DS.red) }
            Hint(L10n.scheduleIosRunOnceNowDoesNotCountTowardAutomaticRuns())
            TextAction(L10n.commonCancel(), alignment: .center, action: onClose)
        }
    }
    private func save() {
        do { onSave(try draft.trigger()); onClose() } catch { self.error = errorText(error) }
    }
}
