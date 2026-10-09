// A computer's daemon settings as its page reads and changes them
// (architecture §13): the approval timeout, the defaults new projects start
// from, and where the computer keeps uploaded images. They are read and
// written with sealed machine commands, so every answer may also not come:
// a save whose outcome is unknown is never called failed. The page reads
// the settings again and says what the computer now has.
import Foundation
import Observation
import RACore
import RAProto

/// The two commands, so that the rules are tested without a computer.
struct MachineSettingsCalls: Sendable {
    var read: @Sendable () async throws -> PBGetSettingsResponse
    /// `clean` also clears the reclaimable images and caches.
    var update: @Sendable (_ settings: PBDaemonSettings, _ clean: Bool) async throws -> PBUpdateSettingsResponse

    static func remote(_ remote: Remote, machine: String) -> MachineSettingsCalls {
        MachineSettingsCalls(
            read: {
                var cmd = PBCommand()
                cmd.getSettings = PBGetSettingsRequest()
                let result = try await remote.machineCommand(machine, cmd)
                guard case .getSettings(let reply)? = result.kind else { throw CommandError("internal", "意外的回复") }
                return reply
            },
            update: { settings, clean in
                var cmd = PBCommand()
                cmd.updateSettings = .with {
                    $0.settings = settings
                    $0.cleanImages = clean
                }
                let result = try await remote.machineCommand(machine, cmd)
                guard case .updateSettings(let reply)? = result.kind else { throw CommandError("internal", "意外的回复") }
                return reply
            })
    }
}

@MainActor
@Observable
final class MachineSettingsModel {
    /// The settings as the computer last said them; nil until read.
    private(set) var settings: PBDaemonSettings?
    private(set) var imageStorage: PBImageStorageUsage?
    /// A read is on its way.
    private(set) var reading = false
    /// Why the last read failed; empty once one worked.
    private(set) var readError = ""
    /// A change is on its way; nothing else is sent meanwhile.
    private(set) var saving = false
    /// Why the last change did not go through, or what is not known about it.
    private(set) var saveError = ""
    /// The last change may or may not have been applied, and reading again did not tell.
    private(set) var saveUnknown = false

    @ObservationIgnored private let calls: MachineSettingsCalls
    /// Bumped by every read and save: an answer that arrives after a newer one was asked for is dropped.
    @ObservationIgnored private var asked = 0

    init(calls: MachineSettingsCalls) {
        self.calls = calls
    }

    /// Reads the settings. What is shown stays until the answer comes, so a retry does not blank the page.
    func read() async {
        asked += 1
        let mine = asked
        reading = true
        readError = ""
        do {
            let reply = try await calls.read()
            guard mine == asked else { return }
            take(reply.settings, reply.hasImageStorage ? reply.imageStorage : nil)
            // What the computer has is known again.
            if saveUnknown {
                saveUnknown = false
                saveError = ""
            }
        } catch is CancellationError {
            // The page went away.
        } catch {
            guard mine == asked else { return }
            readError = errorText(error)
        }
        if mine == asked { reading = false }
    }

    /// Sends `next` as the computer's settings. On an answer that did not
    /// come, the settings are read once more: they say whether the change
    /// was applied, which the lost answer could not.
    func save(_ next: PBDaemonSettings, clean: Bool = false) async {
        guard !saving, settings != nil else { return }
        asked += 1
        let mine = asked
        saving = true
        reading = false
        saveError = ""
        saveUnknown = false
        defer { if mine == asked { saving = false } }
        do {
            let reply = try await calls.update(next, clean)
            guard mine == asked else { return }
            take(reply.settings, reply.hasImageStorage ? reply.imageStorage : nil)
        } catch is CancellationError {
            // The page went away; the next visit reads what the computer has.
        } catch {
            guard mine == asked else { return }
            guard (error as? CommandError)?.unknown == true else {
                saveError = errorText(error)
                return
            }
            do {
                let reply = try await calls.read()
                guard mine == asked else { return }
                take(reply.settings, reply.hasImageStorage ? reply.imageStorage : nil)
                // A cleaning leaves no trace in the settings: only the sizes say, and they are shown.
                if reply.settings != next { saveError = "这次修改没有保存上，电脑上还是原来的设置" }
            } catch {
                guard mine == asked else { return }
                saveUnknown = true
                saveError = "不确定电脑有没有保存这次修改，重新读取后以电脑上的为准"
            }
        }
    }

    private func take(_ settings: PBDaemonSettings, _ storage: PBImageStorageUsage?) {
        self.settings = settings
        if let storage { imageStorage = storage }
    }

    #if DEBUG
    /// A fixed state to look at (`SettingsDemo`).
    func show(settings: PBDaemonSettings?, storage: PBImageStorageUsage? = nil, reading: Bool = false, readError: String = "", saveError: String = "", saveUnknown: Bool = false) {
        self.settings = settings
        imageStorage = storage
        self.reading = reading
        self.readError = readError
        self.saveError = saveError
        self.saveUnknown = saveUnknown
    }
    #endif
}

// MARK: Edits

/// The approval timeouts offered, in minutes.
let approvalTimeoutMinutes = [2, 5, 10, 30]

/// The limits offered for a computer's uploaded images.
let imageQuotaChoices: [Int64] = [256 << 20, 1 << 30, 5 << 30]

/// The approval modes a default may be: none (the CLI's own), 询问 or 自动. 计划 is a passing state and 完全放行 is chosen per project.
let defaultModeChoices: [Mode?] = [nil, .ask, .auto]

extension PBDaemonSettings {
    /// A CLI's defaults for new projects: Claude Code's are apart, the plain fields are Codex's.
    func defaults(for agent: PBAgent) -> PBAgentDefaults {
        if effectiveAgent(agent) == .claude { return claude }
        return .with {
            $0.model = defaultModel
            $0.effort = defaultEffort
            $0.mode = defaultMode
        }
    }

    func with(model: String, effort: String, for agent: PBAgent) -> PBDaemonSettings {
        var s = self
        if effectiveAgent(agent) == .claude {
            s.claude.model = model
            s.claude.effort = effort
        } else {
            s.defaultModel = model
            s.defaultEffort = effort
        }
        return s
    }

    func with(mode: Mode?, for agent: PBAgent) -> PBDaemonSettings {
        var s = self
        let proto = mode?.proto ?? .unspecified
        if effectiveAgent(agent) == .claude { s.claude.mode = proto } else { s.defaultMode = proto }
        return s
    }

    func with(timeoutMinutes: Int) -> PBDaemonSettings {
        var s = self
        s.approvalTimeoutSeconds = Int32(timeoutMinutes * 60)
        return s
    }

    func with(imageQuota: Int64) -> PBDaemonSettings {
        var s = self
        s.imageUploadQuotaBytes = imageQuota
        return s
    }
}

/// The approval timeout as its row writes it: whole minutes, or seconds when it is not a round number of them.
func timeoutText(seconds: Int32) -> String {
    seconds % 60 == 0 ? "\(seconds / 60) 分钟" : "\(seconds) 秒"
}

/// What a computer's uploaded images take, with what is worth saying of it: sizes that are not zero.
func uploadsText(_ usage: PBImageStorageUsage) -> String {
    var parts = [imageBytes(usage.uploadBytes)]
    if usage.reclaimableBytes > 0 { parts.append("可清理 \(imageBytes(usage.reclaimableBytes))") }
    if usage.reservedBytes > 0 { parts.append("正在上传 \(imageBytes(usage.reservedBytes))") }
    return parts.joined(separator: " · ")
}
