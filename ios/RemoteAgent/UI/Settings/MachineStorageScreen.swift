import RACore
import RAProto
import SwiftUI

/// Dedicated Attachment Storage management screen (`.machineStorage(id:)`), reachable
/// from Computer Screen -> 附件存储, HomeDiskReminders, and MachineStatusScreen disk card.
struct MachineStorageScreen: View {
    let id: String
    @Environment(AccountContext.self) private var context

    var body: some View {
        let host = context.coreHost
        let current = context.core.machineSettings
        let vm = current.machineID == id ? current : PBMachineSettingsViewModel()
        let fallbackOnline = context.core.settings.machines.first { $0.machineID == id }?.online ?? false
        let online = vm.machineID == id ? vm.online : fallbackOnline
        let settingsKey = "\(id):\(online):\(context.core.settings.online)"
        let refreshAction: (@MainActor @Sendable () async -> Void)? = if vm.editable {
            { @Sendable in await host.openMachineSettings(machineID: id) }
        } else {
            nil
        }

        MachineStorageContent(
            vm: vm,
            onRefresh: refreshAction,
            onRetry: { Task { await host.openMachineSettings(machineID: id) } },
            onSelectQuota: { bytes in
                context.dispatch {
                    $0.updateMachineDaemonSettings = .with {
                        $0.machineID = id
                        $0.imageUploadQuotaBytes = bytes
                    }
                }
            },
            onClear: {
                context.dispatch {
                    $0.updateMachineDaemonSettings = .with {
                        $0.machineID = id
                        $0.cleanImages = true
                    }
                }
            }
        )
        .task(id: settingsKey) { await host.openMachineSettings(machineID: id) }
    }
}

struct MachineStorageContent: View {
    let vm: PBMachineSettingsViewModel
    let onRefresh: (@MainActor @Sendable () async -> Void)?
    let onRetry: () -> Void
    let onSelectQuota: (Int64) -> Void
    let onClear: () -> Void

    @State private var pickingQuota = false
    @State private var confirmingClear = false

    var body: some View {
        let title = L10n.settingsAttachmentStorage()
        let sub = vm.found ? vm.name.or(vm.machineID) : nil

        SettingsPage(refresh: onRefresh) {
            if !vm.found {
                PageNote(L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
            } else {
                content(vm)
            }
        }
        .navBar(title, subtitle: sub)
        .kitSheet(isPresented: $pickingQuota) {
            quotaPickerSheet(vm.attachmentStorage)
        }
        .confirm(
            isPresented: $confirmingClear,
            title: L10n.settingsIosClearReclaimableImages(),
            text: L10n.settingsIosClearsReclaimableOriginalsAndCacheUnsyncedFullSizeImages(),
            confirm: L10n.settingsClearSettings()
        ) {
            onClear()
        }
    }

    @ViewBuilder private func content(_ vm: PBMachineSettingsViewModel) -> some View {
        let removed = vm.removedBanner.rendered()
        let banner = vm.offlineOrMismatchBanner.rendered()
        let readError = vm.settingsError.rendered()
        let saveErr = vm.saveError.rendered()

        if !removed.isEmpty {
            GroupBanner(removed, tone: .error)
        } else if !banner.isEmpty {
            GroupBanner(banner, tone: vm.protocolMismatch != 0 ? .warn : .plain)
        }
        if vm.saveUnknown {
            GroupBanner(
                saveErr.or(L10n.settingsIosUnsureWhetherTheComputerSavedThisChangeReloadTo()),
                tone: .warn,
                action: L10n.settingsIosReload(),
                onAction: onRetry
            )
        } else if !saveErr.isEmpty {
            GroupBanner(saveErr, tone: .error)
        }

        if vm.hasAttachmentStorage && vm.attachmentStorage.loaded {
            loadedStorage(vm, s: vm.attachmentStorage)
        } else if vm.settingsLoading {
            LoadingRow(inset: 30)
        } else if !readError.isEmpty {
            GroupBanner(readError, tone: .error, action: L10n.commonRetry(), onAction: onRetry)
        } else if !vm.editable && vm.protocolMismatch == 0 && !vm.online {
            GroupNote(L10n.settingsIosIsOfflineConnectToReadAndEditNewSession(value: vm.name.or(vm.machineID)))
        }
    }

    @ViewBuilder private func loadedStorage(_ vm: PBMachineSettingsViewModel, s: PBAttachmentStorageView) -> some View {
        let busy = vm.savePending || !vm.editable
        let enabled = !busy
        let quotaLabel = s.limitLabel.rendered()
            .or(s.quotaChoices.first(where: \.selected)?.label.rendered() ?? wholeBytes(s.selectedQuotaBytes))
        let usedText = s.usedTotalText.rendered().or("—")
        let uploaded = s.uploadedText.rendered().or("—")
        let uploadedDetail = s.uploadedDetail.rendered()
        let previewCache = s.previewCacheText.rendered().or("—")
        let clearLabel = s.clearButtonLabel_p.rendered().or(L10n.settingsNothingToClear())
        let footer = s.footerText.rendered().or(L10n.settingsStoragePageFooter())

        GroupCard {
            VStack(alignment: .leading, spacing: 12) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(L10n.settingsStorageUsedHeadline(used: usedText))
                        .font(.body.weight(.semibold))
                        .foregroundStyle(DS.ink)
                    Spacer(minLength: 8)
                    Text(L10n.settingsStorageLimitHeadline(limit: quotaLabel))
                        .font(.subheadline)
                        .foregroundStyle(s.summaryWarn ? DS.orangeInk : DS.ink2)
                }

                StorageCompositionBar(
                    uploadedPercent: Int(s.uploadedPercent),
                    previewPercent: Int(s.previewPercent),
                    reservePercent: s.showEmergencyReserve ? Int(s.reservePercent) : 0
                )

                HStack(spacing: 14) {
                    StorageLegendDot(color: DS.ink, label: L10n.settingsStorageLegendUploaded())
                    StorageLegendDot(color: DS.ink3, label: L10n.settingsStorageLegendPreview())
                    if s.showEmergencyReserve {
                        StorageLegendDot(color: DS.orange, label: L10n.settingsStorageLegendReserve())
                    }
                    Spacer(minLength: 0)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 14)
        }

        GroupLabel(L10n.settingsStorageSectionDetails())
        GroupCard {
            GRow(
                L10n.settingsUploadedImagesAndFiles(),
                sub: uploadedDetail.isEmpty ? nil : uploadedDetail,
                value: uploaded,
                valueLines: nil
            )
            GRow(L10n.settingsPreviewCache(), value: previewCache)
            if s.showEmergencyReserve {
                StorageReserveRow(text: s.emergencyReserveText.rendered())
            }
            GRow(L10n.settingsStorageLimitRow(), value: quotaLabel, chevron: true, enabled: enabled) {
                pickingQuota = true
            }
        }

        GroupCard {
            GRow(
                L10n.settingsStorageClearRow(),
                value: clearLabel,
                action: true,
                enabled: enabled && s.canClear && !s.cleaning
            ) {
                confirmingClear = true
            }
        }

        if vm.savePending || s.cleaning {
            LoadingRow(L10n.settingsIosSaving(), inset: 30).padding(.bottom, 4)
        }

        GroupNote(footer)
    }

    @ViewBuilder private func quotaPickerSheet(_ s: PBAttachmentStorageView) -> some View {
        let choices = s.quotaChoices
        SheetHead(L10n.settingsStorageLimit())
        ForEach(Array(choices.enumerated()), id: \.offset) { idx, c in
            ListItem(
                c.label.rendered().or(wholeBytes(c.bytes)),
                checked: c.selected,
                last: idx == choices.count - 1
            ) {
                pickingQuota = false
                onSelectQuota(c.bytes)
            }
        }
    }
}

private struct StorageCompositionBar: View {
    let uploadedPercent: Int
    let previewPercent: Int
    let reservePercent: Int

    var body: some View {
        let u = max(0, uploadedPercent)
        let p = max(0, previewPercent)
        let r = max(0, reservePercent)
        GeometryReader { geo in
            let w = geo.size.width
            ZStack(alignment: .leading) {
                Capsule().fill(DS.fill)
                HStack(spacing: 1) {
                    if u > 0 {
                        Rectangle()
                            .fill(DS.ink)
                            .frame(width: max(3, w * CGFloat(min(100, u)) / 100))
                    }
                    if p > 0 {
                        Rectangle()
                            .fill(DS.ink3)
                            .frame(width: max(3, w * CGFloat(min(100, p)) / 100))
                    }
                    if r > 0 {
                        Rectangle()
                            .fill(DS.orange)
                            .frame(width: max(3, w * CGFloat(min(100, r)) / 100))
                    }
                    Spacer(minLength: 0)
                }
                .clipShape(Capsule())
            }
        }
        .frame(height: 10)
        .accessibilityHidden(true)
    }
}

private struct StorageLegendDot: View {
    let color: Color
    let label: String

    var body: some View {
        HStack(spacing: 5) {
            Circle().fill(color).frame(width: 7, height: 7)
            Text(label).font(.caption).foregroundStyle(DS.ink2)
        }
    }
}

private struct StorageReserveRow: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(L10n.machineStatusEmergencyReserve()).font(.body).foregroundStyle(DS.ink)
                Spacer(minLength: 0)
                if !text.isEmpty {
                    Text(text).font(.subheadline).foregroundStyle(DS.ink2)
                }
            }
            Text(L10n.machineStatusEmergencyReserveNote()).font(.footnote).foregroundStyle(DS.ink2)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}
