#if DEBUG
import Foundation
import RAProto
import SwiftProtobuf

/// Loads ViewModel snapshots from `testdata/screens/*.golden.pb` for SwiftUI `#Preview`s
/// and UI rendering tests.
enum ScreenGoldens {
    private static let repoRoot: URL = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent() // Demo
        .deletingLastPathComponent() // RemoteAgent
        .deletingLastPathComponent() // ios
        .deletingLastPathComponent() // repo root

    static func viewModels(named name: String) -> [PBCoreViewModel] {
        let url = repoRoot.appendingPathComponent("testdata/screens/\(name).golden.pb")
        let data: Data
        do {
            data = try Data(contentsOf: url)
        } catch {
            fatalError("Failed to read screen golden \(url.path): \(error)")
        }
        let golden: PBScreenGoldenFile
        do {
            golden = try PBScreenGoldenFile(serializedBytes: data)
        } catch {
            fatalError("Failed to decode screen golden \(url.path): \(error)")
        }

        var out: [PBCoreViewModel] = []
        var seen = Set<String>()
        for step in golden.steps {
            for view in step.views where view.hasViewModel {
                precondition(seen.insert(view.name).inserted, "Duplicate golden view '\(view.name)' in \(name).golden.pb")
                out.append(view.viewModel)
            }
        }
        precondition(!out.isEmpty, "Screen golden \(name).golden.pb contained no view models")
        return out
    }

    private static func pick<T>(_ all: [T], index: Int?, kind: String, name: String) -> T {
        if let index {
            precondition(all.indices.contains(index), "Screen golden \(name).golden.pb missing \(kind) at index \(index) (count=\(all.count))")
            return all[index]
        }
        guard let last = all.last else {
            fatalError("Screen golden \(name).golden.pb contained no \(kind) views")
        }
        return last
    }

    static func home(_ name: String, index: Int? = nil) -> PBCoreHomeViewModel {
        let all = viewModels(named: name).compactMap { vm -> PBCoreHomeViewModel? in
            guard case .home(let h)? = vm.view else { return nil }
            return h
        }
        return pick(all, index: index, kind: "home", name: name)
    }

    static func projectDetail(_ name: String) -> PBCoreProjectDetailView {
        let all = viewModels(named: name).compactMap { vm -> PBCoreProjectViewModel? in
            guard case .project(let p)? = vm.view, p.kind == .detail else { return nil }
            return p
        }
        return pick(all, index: nil, kind: "projectDetail", name: name).detail
    }

    static func manageProjects(_ name: String) -> PBCoreManageProjectsView {
        let all = viewModels(named: name).compactMap { vm -> PBCoreProjectViewModel? in
            guard case .project(let p)? = vm.view, p.kind == .manage else { return nil }
            return p
        }
        return pick(all, index: nil, kind: "manageProjects", name: name).manage
    }

    static func newSession(_ name: String, index: Int? = nil) -> PBCoreNewSessionViewModel {
        let all = viewModels(named: name).compactMap { vm -> PBCoreNewSessionViewModel? in
            guard case .newSession(let ns)? = vm.view else { return nil }
            return ns
        }
        return pick(all, index: index, kind: "newSession", name: name)
    }

    static func sessions(_ name: String) -> [PBSessionViewModel] {
        viewModels(named: name).compactMap { vm -> PBSessionViewModel? in
            guard case .session(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func session(_ name: String, index: Int? = nil) -> PBSessionViewModel {
        pick(sessions(name), index: index, kind: "session", name: name)
    }

    static func schedulesList(_ name: String) -> [PBSchedulesViewModel] {
        viewModels(named: name).compactMap { vm -> PBSchedulesViewModel? in
            guard case .schedules(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func schedules(_ name: String, index: Int? = nil) -> PBSchedulesViewModel {
        pick(schedulesList(name), index: index, kind: "schedules", name: name)
    }

    static func scheduleDetails(_ name: String) -> [PBScheduleDetailViewModel] {
        viewModels(named: name).compactMap { vm -> PBScheduleDetailViewModel? in
            guard case .scheduleDetail(let d)? = vm.view else { return nil }
            return d
        }
    }

    static func scheduleDetail(_ name: String, index: Int? = nil) -> PBScheduleDetailViewModel {
        pick(scheduleDetails(name), index: index, kind: "scheduleDetail", name: name)
    }

    static func scheduleForms(_ name: String) -> [PBScheduleFormViewModel] {
        viewModels(named: name).compactMap { vm -> PBScheduleFormViewModel? in
            guard case .scheduleForm(let f)? = vm.view else { return nil }
            return f
        }
    }

    static func scheduleForm(_ name: String, index: Int? = nil) -> PBScheduleFormViewModel {
        pick(scheduleForms(name), index: index, kind: "scheduleForm", name: name)
    }

    static func scheduleStrips(_ name: String) -> [PBScheduleSessionStripViewModel] {
        viewModels(named: name).compactMap { vm -> PBScheduleSessionStripViewModel? in
            guard case .scheduleSessionStrip(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func settingsAll(_ name: String) -> [PBSettingsViewModel] {
        viewModels(named: name).compactMap { vm -> PBSettingsViewModel? in
            guard case .settings(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func devicesAll(_ name: String) -> [PBDevicesViewModel] {
        viewModels(named: name).compactMap { vm -> PBDevicesViewModel? in
            guard case .devices(let d)? = vm.view else { return nil }
            return d
        }
    }

    static func clientDetails(_ name: String) -> [PBClientDetailViewModel] {
        viewModels(named: name).compactMap { vm -> PBClientDetailViewModel? in
            guard case .clientDetail(let c)? = vm.view else { return nil }
            return c
        }
    }

    static func machineSettingsAll(_ name: String) -> [PBMachineSettingsViewModel] {
        viewModels(named: name).compactMap { vm -> PBMachineSettingsViewModel? in
            guard case .machineSettings(let ms)? = vm.view else { return nil }
            return ms
        }
    }

    static func machineStatuses(_ name: String) -> [PBMachineStatusViewModel] {
        viewModels(named: name).compactMap { vm -> PBMachineStatusViewModel? in
            guard case .machineStatus(let ms)? = vm.view else { return nil }
            return ms
        }
    }

    static func quotas(_ name: String) -> [PBQuotaViewModel] {
        viewModels(named: name).compactMap { vm -> PBQuotaViewModel? in
            guard case .quota(let q)? = vm.view else { return nil }
            return q
        }
    }
}
#endif
