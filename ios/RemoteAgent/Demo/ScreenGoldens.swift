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
}
#endif
