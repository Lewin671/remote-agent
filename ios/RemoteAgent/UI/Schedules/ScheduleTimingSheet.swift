import RACore
import RAProto
import SwiftUI

struct ScheduleTimingSheet: View {
    var submitOnApply: Bool = false
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context

    var body: some View {
        let form = context.core.scheduleForm, formKey = form.formKey
        ScheduleTimingContent(
            draft: form.timing, mutation: form.mutation, enabled: form.editable, submitOnApply: submitOnApply,
            onUpdate: { fill in context.dispatch { $0.scheduleFormUpdateTiming = .with { $0.formKey = formKey; fill(&$0) } } },
            onApply: { fill in
                let action: PBScheduleFormUpdateTimingAction = .with { $0.formKey = formKey; fill(&$0); $0.apply = true }
                let host = context.coreHost, screens = context.core
                Task { @MainActor in
                    await host.dispatch { @Sendable ev in ev.scheduleFormUpdateTiming = action }
                    let updated = screens.scheduleForm.timing
                    guard updated.timingApplied && !updated.hasTimingError else { return }
                    if submitOnApply {
                        await host.dispatch { @Sendable ev in ev.scheduleFormSubmit = .with { $0.formKey = formKey; $0.value = false } }
                    } else { onClose() }
                }
            },
            onRetryMutation: { context.dispatch { $0.scheduleRetryMutation = .with { $0.formKey = formKey } } },
            onAbandonMutation: { context.dispatch { $0.scheduleAbandonMutation = .with { $0.formKey = formKey } } },
            onCancel: {
                context.dispatch {
                    if submitOnApply { $0.scheduleFormClose = .with { $0.formKey = formKey; $0.value = true } }
                    else { $0.scheduleFormUpdateTiming = .with { $0.formKey = formKey; $0.resetDraft = true } }
                }
                onClose()
            }
        )
    }
}

struct ScheduleTimingContent: View {
    let draft: PBScheduleTimingDraftView
    var mutation: PBScheduleMutationView = PBScheduleMutationView()
    var enabled: Bool = true
    var submitOnApply: Bool = false
    let onUpdate: ((inout PBScheduleFormUpdateTimingAction) -> Void) -> Void
    let onApply: ((inout PBScheduleFormUpdateTimingAction) -> Void) -> Void
    var onRetryMutation: () -> Void = {}
    var onAbandonMutation: () -> Void = {}
    let onCancel: () -> Void

    @State private var intervalInput = ""
    @State private var countInput = ""
    @State private var zoneSearch = ""
    @State private var pickingZone = false

    private static let kinds: [PBScheduleTimingKind] = [.once, .daily, .weekly, .interval]
    private static let units: [PBScheduleIntervalUnit] = [.minutes, .hours, .days, .seconds]
    private static let ends: [PBScheduleTimingEnd] = [.never, .count, .date]
    private static let commonZoneIDs = ["Asia/Shanghai", "Asia/Tokyo"]
    private static let posix = Locale(identifier: "en_US_POSIX")

    var body: some View {
        let busy = mutation.busy, sheetEnabled = enabled && !busy
        let units = [L10n.scheduleIosMinutes(), L10n.scheduleIosHours(), L10n.scheduleIosDays(), L10n.scheduleIosSeconds()]
        let weekdays = [L10n.scheduleMon(), L10n.scheduleTue(), L10n.scheduleWed(), L10n.scheduleThu(), L10n.scheduleFri(), L10n.scheduleSat(), L10n.scheduleSun()]
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if submitOnApply { ScheduleMutationBanner(mutation: mutation, onRetry: onRetryMutation, onAbandon: onAbandonMutation) }
                    if draft.hasTimingError { Banner(draft.timingError.rendered(), tone: .error) }
                    SegPick(
                        [L10n.scheduleOnceSchedules(), L10n.scheduleDaily(), L10n.scheduleWeeklyOn(), L10n.scheduleEvery()],
                        selected: Binding(
                            get: { draft.afterTurnUnselected ? -1 : (Self.kinds.firstIndex(of: draft.kind) ?? 0) },
                            set: { idx in if Self.kinds.indices.contains(idx) { let k = Self.kinds[idx]; onUpdate { $0.kind = k } } }
                        )
                    ).disabled(!sheetEnabled)

                    if draft.afterTurnUnselected && draft.hasAfterTurnHint {
                        Hint(draft.afterTurnHint.rendered())
                    } else {
                        if draft.kind == .once {
                            GroupCard {
                                DatePicker(L10n.scheduleWhen(), selection: Binding(
                                    get: { parse("\(draft.date) \(draft.time)", format: "yyyy-MM-dd HH:mm") },
                                    set: { let (d, t) = (format($0, "yyyy-MM-dd"), format($0, "HH:mm")); onUpdate { $0.date = d; $0.time = t } }
                                ), displayedComponents: [.date, .hourAndMinute])
                                    .environment(\.timeZone, activeTimeZone).padding(.horizontal, 14).padding(.vertical, 8).disabled(!sheetEnabled)
                            }
                        } else if draft.kind == .daily || draft.kind == .weekly {
                            GroupCard {
                                DatePicker(L10n.scheduleTime(), selection: Binding(
                                    get: { parse(draft.time, format: "HH:mm") },
                                    set: { let t = format($0, "HH:mm"); onUpdate { $0.time = t } }
                                ), displayedComponents: .hourAndMinute)
                                    .environment(\.timeZone, activeTimeZone).padding(.horizontal, 14).padding(.vertical, 8).disabled(!sheetEnabled)
                                if draft.showJitter && !draft.jitterOptions.isEmpty {
                                    jitterRow(enabled: sheetEnabled)
                                }
                            }
                            if draft.showJitter && draft.hasJitterHint { Hint(draft.jitterHint.rendered()) }
                            if draft.kind == .daily {
                                GroupCard {
                                    ToggleRow(L10n.scheduleWeekdaysOnly(), isOn: Binding(get: { draft.workdays }, set: { v in onUpdate { $0.workdays = v } }))
                                        .disabled(!sheetEnabled)
                                }
                            }
                            if draft.kind == .weekly { weekdayPicker(names: weekdays, enabled: sheetEnabled) }
                        } else if draft.kind == .interval {
                            GroupCard {
                                HStack(spacing: 10) {
                                    Text(L10n.scheduleEvery()).font(.body).ink(DS.ink)
                                    TextField("30", text: $intervalInput).keyboardType(.numberPad).multilineTextAlignment(.trailing)
                                        .frame(width: 64).disabled(!sheetEnabled)
                                        .onChange(of: intervalInput) { _, val in if val != draft.interval { onUpdate { $0.interval = val } } }
                                    Spacer()
                                }.padding(.horizontal, 14).padding(.vertical, 10)
                                Picker(L10n.scheduleEvery(), selection: Binding(
                                    get: { Self.units.firstIndex(of: draft.unit) ?? 0 },
                                    set: { idx in let u = Self.units[idx]; onUpdate { $0.unit = u } }
                                )) { ForEach(units.indices, id: \.self) { Text(units[$0]).tag($0) } }
                                    .pickerStyle(.segmented).padding(.horizontal, 14).padding(.bottom, 10).disabled(!sheetEnabled)
                            }
                            if draft.showIntervalHint && draft.hasIntervalHint { Hint(draft.intervalHint.rendered()) }
                        }
                        if draft.kind != .once { endSection(enabled: sheetEnabled) }
                        if draft.kind != .interval {
                            GroupCard { GRow(L10n.scheduleTimeZone(), value: zoneLabel(draft.zone), chevron: sheetEnabled) { if sheetEnabled { pickingZone = true } } }
                        }
                    }
                }.padding(.horizontal, 16).padding(.vertical, 12)
            }
            .background(DS.gbg.ignoresSafeArea())
            .navigationTitle(L10n.scheduleWhen()).navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) { Button(L10n.commonCancel()) { if !busy { onCancel() } }.disabled(busy) }
                ToolbarItem(placement: .topBarTrailing) {
                    Button(submitOnApply && busy ? L10n.commonIosSaving() : L10n.commonDone()) {
                        let iv = intervalInput, ct = countInput, kind = draft.kind, end = draft.end
                        onApply { if kind == .interval { $0.interval = iv }; if kind != .once && end == .count { $0.count = ct } }
                    }.fontWeight(.semibold).disabled(!(sheetEnabled && draft.timingDoneEnabled))
                }
            }
            .kitSheet(isPresented: $pickingZone, size: .large) { zonePickerSheet }
            .onChange(of: draft.interval, initial: true) { _, val in if intervalInput != val { intervalInput = val } }
            .onChange(of: draft.count, initial: true) { _, val in if countInput != val { countInput = val } }
        }
    }

    @ViewBuilder private func jitterRow(enabled: Bool) -> some View {
        let selected = draft.jitterOptions.first(where: { $0.selected })?.minutes ?? draft.jitterOptions[0].minutes
        HStack(spacing: 10) {
            Text(draft.jitterLabel.rendered()).font(.body).ink(DS.ink)
            Spacer()
            Picker(draft.jitterLabel.rendered(), selection: Binding(
                get: { selected },
                set: { v in onUpdate { $0.jitterMinutes = v } }
            )) {
                ForEach(draft.jitterOptions, id: \.minutes) { opt in
                    Text(opt.label.rendered()).tag(opt.minutes)
                }
            }
            .pickerStyle(.menu).labelsHidden().tint(DS.ink2).disabled(!enabled)
        }.padding(.horizontal, 14).padding(.vertical, 8)
    }

    private func weekdayPicker(names: [String], enabled: Bool) -> some View {
        let selected = Set(draft.weekdays)
        return HStack(spacing: 4) {
            ForEach(1...7, id: \.self) { day in
                let isSelected = selected.contains(Int32(day))
                Button {
                    let next = (isSelected ? selected.subtracting([Int32(day)]) : selected.union([Int32(day)])).sorted()
                    onUpdate { $0.replaceWeekdays = true; $0.weekdays = next }
                } label: {
                    Text(names[day - 1]).font(isSelected ? .subheadline.weight(.semibold) : .subheadline)
                        .ink(isSelected ? DS.ink : DS.ink2).frame(maxWidth: .infinity, minHeight: DS.minHit)
                        .background(isSelected ? DS.paper : Color.clear, in: RoundedRectangle(cornerRadius: 10))
                }.buttonStyle(.plain).disabled(!enabled)
            }
        }.padding(3).background(DS.fill2, in: RoundedRectangle(cornerRadius: 12))
    }

    @ViewBuilder private func endSection(enabled: Bool) -> some View {
        GroupCard {
            Picker(L10n.sessionEnd(), selection: Binding(
                get: { Self.ends.firstIndex(of: draft.end) ?? 0 },
                set: { idx in let e = Self.ends[idx]; onUpdate { $0.end = e } }
            )) {
                Text(L10n.scheduleNever()).tag(0)
                Text(L10n.scheduleAfterARunCount()).tag(1)
                Text(L10n.scheduleEndDate()).tag(2)
            }.pickerStyle(.segmented).padding(12).disabled(!enabled)
            if draft.end == .count {
                HStack(spacing: 10) {
                    Text(L10n.scheduleAfterARunCount()).font(.body).ink(DS.ink); Spacer()
                    TextField("5", text: $countInput).keyboardType(.numberPad).multilineTextAlignment(.trailing)
                        .frame(width: 72).disabled(!enabled)
                        .onChange(of: countInput) { _, val in if val != draft.count { onUpdate { $0.count = val } } }
                }.padding(.horizontal, 14).padding(.bottom, 12)
            } else if draft.end == .date {
                DatePicker(L10n.scheduleEndDate(), selection: Binding(
                    get: { parse(draft.untilDate, format: "yyyy-MM-dd") },
                    set: { let d = format($0, "yyyy-MM-dd"); onUpdate { $0.untilDate = d } }
                ), displayedComponents: .date)
                    .environment(\.timeZone, activeTimeZone).padding(.horizontal, 14).padding(.bottom, 12).disabled(!enabled)
            }
        }
    }

    private var zonePickerSheet: some View {
        let query = zoneSearch.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let defaults = (Self.commonZoneIDs + [TimeZone.current.identifier]).reduce(into: [String]()) { if !$0.contains($1) { $0.append($1) } }
        let all = defaults + TimeZone.knownTimeZoneIdentifiers.filter { !defaults.contains($0) }
        let filtered = query.isEmpty ? all : all.filter { $0.lowercased().contains(query) || zoneLabel($0).lowercased().contains(query) }
        return NavigationStack {
            List(filtered, id: \.self) { id in
                ListItem(zoneLabel(id), desc: id, checked: id == draft.zone) { onUpdate { $0.zone = id }; pickingZone = false }
            }
            .listStyle(.plain).searchable(text: $zoneSearch)
            .navigationTitle(L10n.scheduleTimeZone()).navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .topBarLeading) { Button(L10n.commonClose()) { pickingZone = false } } }
        }
    }

    private var activeTimeZone: TimeZone { TimeZone(identifier: draft.zone) ?? .current }
    private func zoneLabel(_ id: String) -> String { id == Self.commonZoneIDs[0] ? L10n.scheduleBeijingTime() : id == Self.commonZoneIDs[1] ? L10n.scheduleTokyoTime() : id }
    private func formatter(_ fmt: String) -> DateFormatter { let f = DateFormatter(); f.locale = Self.posix; f.timeZone = activeTimeZone; f.dateFormat = fmt; return f }
    private func parse(_ text: String, format: String) -> Date { formatter(format).date(from: text) ?? Date() }
    private func format(_ date: Date, _ fmt: String) -> String { formatter(fmt).string(from: date) }
}
