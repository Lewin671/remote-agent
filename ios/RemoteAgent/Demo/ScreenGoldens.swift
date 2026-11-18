#if DEBUG
import Foundation
import RAProto
import SwiftProtobuf

/// Loads ViewModel snapshots from `testdata/screens/*.golden.json` for SwiftUI `#Preview`s
/// and UI rendering tests.
enum ScreenGoldens {
    private static let repoRoot: URL = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent() // Demo
        .deletingLastPathComponent() // RemoteAgent
        .deletingLastPathComponent() // ios
        .deletingLastPathComponent() // repo root

    static func viewModels(named name: String) -> [PBCoreViewModel] {
        let url = repoRoot.appendingPathComponent("testdata/screens/\(name).golden.json")
        guard let data = try? Data(contentsOf: url),
              let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let steps = root["steps"] as? [[String: Any]]
        else { return [] }

        var options = JSONDecodingOptions()
        options.ignoreUnknownFields = true

        var out: [PBCoreViewModel] = []
        for step in steps {
            guard let views = step["views"] as? [String: Any] else { continue }
            for key in views.keys.sorted() {
                guard let obj = views[key],
                      let json = try? JSONSerialization.data(withJSONObject: obj),
                      let vm = try? PBCoreViewModel(jsonUTF8Data: json, options: options)
                else { continue }
                out.append(vm)
            }
        }
        return out
    }

    static func home(_ name: String, index: Int? = nil) -> PBCoreHomeViewModel {
        let all = viewModels(named: name).compactMap { vm -> PBCoreHomeViewModel? in
            guard case .home(let h)? = vm.view else { return nil }
            return h
        }
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBCoreHomeViewModel()
    }

    static func projectDetail(_ name: String) -> PBCoreProjectDetailView {
        let all = viewModels(named: name).compactMap { vm -> PBCoreProjectViewModel? in
            guard case .project(let p)? = vm.view, p.kind == .detail else { return nil }
            return p
        }
        return all.last?.detail ?? PBCoreProjectDetailView()
    }

    static func manageProjects(_ name: String) -> PBCoreManageProjectsView {
        let all = viewModels(named: name).compactMap { vm -> PBCoreProjectViewModel? in
            guard case .project(let p)? = vm.view, p.kind == .manage else { return nil }
            return p
        }
        return all.last?.manage ?? PBCoreManageProjectsView()
    }

    static func newSession(_ name: String, index: Int? = nil) -> PBCoreNewSessionViewModel {
        let all = viewModels(named: name).compactMap { vm -> PBCoreNewSessionViewModel? in
            guard case .newSession(let ns)? = vm.view else { return nil }
            return ns
        }
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBCoreNewSessionViewModel()
    }

    static func sessions(_ name: String) -> [PBSessionViewModel] {
        viewModels(named: name).compactMap { vm -> PBSessionViewModel? in
            guard case .session(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func session(_ name: String, index: Int? = nil) -> PBSessionViewModel {
        let all = sessions(name)
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBSessionViewModel()
    }

    static func schedulesList(_ name: String) -> [PBSchedulesViewModel] {
        viewModels(named: name).compactMap { vm -> PBSchedulesViewModel? in
            guard case .schedules(let s)? = vm.view else { return nil }
            return s
        }
    }

    static func schedules(_ name: String, index: Int? = nil) -> PBSchedulesViewModel {
        let all = schedulesList(name)
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBSchedulesViewModel()
    }

    static func scheduleDetails(_ name: String) -> [PBScheduleDetailViewModel] {
        viewModels(named: name).compactMap { vm -> PBScheduleDetailViewModel? in
            guard case .scheduleDetail(let d)? = vm.view else { return nil }
            return d
        }
    }

    static func scheduleDetail(_ name: String, index: Int? = nil) -> PBScheduleDetailViewModel {
        let all = scheduleDetails(name)
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBScheduleDetailViewModel()
    }

    static func scheduleForms(_ name: String) -> [PBScheduleFormViewModel] {
        viewModels(named: name).compactMap { vm -> PBScheduleFormViewModel? in
            guard case .scheduleForm(let f)? = vm.view else { return nil }
            return f
        }
    }

    static func scheduleForm(_ name: String, index: Int? = nil) -> PBScheduleFormViewModel {
        let all = scheduleForms(name)
        if let index, all.indices.contains(index) { return all[index] }
        return all.last ?? PBScheduleFormViewModel()
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
