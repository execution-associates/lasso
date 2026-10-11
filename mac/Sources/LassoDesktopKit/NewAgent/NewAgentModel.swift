import Combine
import Foundation

// Launching an agent from the Mac: the web app's New dialog (⌘O a Git agent,
// ⌘⇧O a Scratch one; src/web/src/components/NewDialog.tsx) over the same
// routes, ending in the new agent's own window.

/// GET /api/agent-config: the creator's defaults and the harness registry.
public struct AgentConfig: Codable, Sendable, Equatable {
    public struct Harness: Codable, Sendable, Equatable, Identifiable {
        public var id: String
        public var label: String
        public var supports_plan_mode: Bool
        public var effort_levels: [String]?
        public var model_suggestions: [String]?
    }

    public var branch_prefix: String?
    public var default_agent: String?
    public var last_repo: String?
    public var last_agent: String?
    public var last_agent_type: String?
    public var harnesses: [Harness]?
}

/// One repository under the host's repos root (GET /api/repos).
public struct RepoEntry: Codable, Sendable, Equatable, Identifiable {
    public var path: String
    public var name: String
    public var last_base_branch: String?
    public var id: String { path }
}

public struct ReposPayload: Codable, Sendable, Equatable {
    public var repos: [RepoEntry]
}

/// GET /api/repo-branches: its branches and default.
public struct RepoBranches: Codable, Sendable, Equatable {
    public var branches: [String]
    public var `default`: String?
}

/// POST /api/create-agent's body (src/agents_create.go createAgentReq).
public struct CreateAgent: Codable, Sendable, Equatable {
    public var host: String
    public var type: String
    public var prompt: String
    public var repo: String?
    public var base_branch: String?
    public var agent: String
    public var model: String?
    public var effort: String?
    public var plan_mode: Bool
    public var advisor = false
}

/// What /api/create-agent answers: the record, with the pane the agent boots in.
public struct CreatedAgent: Codable, Sendable, Equatable {
    public var id: String
    public var title: String
    public var agent: String
    public var work_dir: String?
    public var workspace_id: String?
    public var root_pane: String?
}

/// Where the New Agent form reads from and creates through: lasso, or a fixture.
@MainActor
public protocol NewAgentSource: AnyObject {
    func hosts() async -> [String]
    func config(host: String) async throws -> AgentConfig
    func repos(host: String) async throws -> [RepoEntry]
    func branches(repo: String, host: String) async throws -> RepoBranches
    func create(_ req: CreateAgent) async throws -> CreatedAgent
}

@MainActor
public final class LiveNewAgentSource: NewAgentSource {
    let client: LassoClient
    public init(client: LassoClient) { self.client = client }

    /// local, then every host lasso can drive right now.
    public func hosts() async -> [String] {
        guard let h = try? await client.get("/api/hosts", as: HostsPayload.self) else { return ["local"] }
        return ["local"] + h.hosts.filter { $0.reachable && $0.running && $0.compatible }.map(\.alias)
    }

    public func config(host: String) async throws -> AgentConfig {
        try await client.get("/api/agent-config", query: ["host": host])
    }

    public func repos(host: String) async throws -> [RepoEntry] {
        try await client.get("/api/repos", query: ["host": host], as: ReposPayload.self).repos
    }

    public func branches(repo: String, host: String) async throws -> RepoBranches {
        try await client.get("/api/repo-branches", query: ["path": repo, "host": host])
    }

    public func create(_ req: CreateAgent) async throws -> CreatedAgent {
        try await client.post("/api/create-agent", json: req)
    }
}

/// The New Agent form's state.
@MainActor
public final class NewAgentModel: ObservableObject {
    public enum Kind: String, CaseIterable, Sendable {
        case git, scratch
        public var label: String { self == .git ? "In a repository" : "Scratch" }
    }

    @Published public var kind: Kind
    @Published public var host = "local" { didSet { if host != oldValue { Task { await loadHost() } } } }
    @Published public var repo = "" { didSet { if repo != oldValue { Task { await loadBranches() } } } }
    @Published public var baseBranch = ""
    @Published public var agent = ""
    @Published public var model = ""
    @Published public var effort = ""
    @Published public var planMode = false
    @Published public var prompt = ""
    @Published public private(set) var hosts: [String] = ["local"]
    @Published public private(set) var harnesses: [AgentConfig.Harness] = []
    @Published public private(set) var repos: [RepoEntry] = []
    @Published public private(set) var branches: [String] = []
    @Published public private(set) var busy = false
    @Published public private(set) var error: String?

    /// The repository and branch pickers' own state (open, filter).
    public let repoPicker = PickerState()
    public let branchPicker = PickerState()
    let source: NewAgentSource
    /// The agent was created: open its window.
    public var onCreated: (HostPane) -> Void = { _ in }
    public var onClose: () -> Void = {}

    public init(kind: Kind, source: NewAgentSource) {
        self.kind = kind
        self.source = source
    }

    public var harness: AgentConfig.Harness? { harnesses.first { $0.id == agent } }

    /// The prompt's first line names the agent (its branch, workspace and title).
    public var title: String {
        prompt.split(separator: "\n", omittingEmptySubsequences: true).first.map(String.init)?
            .trimmingCharacters(in: .whitespaces) ?? ""
    }

    public var canCreate: Bool {
        !busy && !title.isEmpty && !agent.isEmpty && (kind == .scratch || !repo.isEmpty)
    }

    public func load() async {
        hosts = await source.hosts()
        await loadHost()
    }

    func loadHost() async {
        do {
            let cfg = try await source.config(host: host)
            harnesses = cfg.harnesses ?? []
            let preferred = [cfg.default_agent, cfg.last_agent].compactMap { $0 }.first { !$0.isEmpty }
            if agent.isEmpty || !harnesses.contains(where: { $0.id == agent }) {
                agent = preferred.flatMap { p in harnesses.first { $0.id == p }?.id } ?? harnesses.first?.id ?? ""
            }
            if kind == .git {
                let all = try await source.repos(host: host)
                // The last repository first, then by name.
                repos = all.filter { $0.path == cfg.last_repo } + all.filter { $0.path != cfg.last_repo }.sorted { $0.name < $1.name }
                if repo.isEmpty || !repos.contains(where: { $0.path == repo }) {
                    repo = cfg.last_repo.flatMap { r in repos.first { $0.path == r }?.path } ?? repos.first?.path ?? ""
                }
            }
            error = nil
        } catch {
            self.error = "Could not read \(host)'s setup: \(error)"
        }
    }

    func loadBranches() async {
        guard kind == .git, !repo.isEmpty else { return }
        do {
            let b = try await source.branches(repo: repo, host: host)
            branches = b.branches
            let last = repos.first { $0.path == repo }?.last_base_branch
            baseBranch = [last, b.default].compactMap { $0 }.first { branches.contains($0) } ?? branches.first ?? ""
        } catch {
            branches = []
        }
    }

    /// The body for /api/create-agent.
    public var request: CreateAgent {
        CreateAgent(
            host: host, type: kind.rawValue, prompt: prompt.trimmingCharacters(in: .whitespacesAndNewlines),
            repo: kind == .git ? repo : nil, base_branch: kind == .git && !baseBranch.isEmpty ? baseBranch : nil,
            agent: agent, model: model.isEmpty ? nil : model, effort: effort.isEmpty ? nil : effort,
            plan_mode: planMode && (harness?.supports_plan_mode ?? false))
    }

    /// Create it, then open its window on the pane it boots in.
    public func create() {
        guard canCreate else { return }
        busy = true
        error = nil
        let req = request
        Task {
            defer { busy = false }
            do {
                let rec = try await source.create(req)
                guard let pane = rec.root_pane, !pane.isEmpty else {
                    error = "lasso created \(rec.title) but named no pane for it"
                    return
                }
                onCreated(
                    HostPane(
                        host: req.host, host_label: req.host, pane_id: pane, workspace_id: rec.workspace_id ?? "",
                        workspace_label: rec.title, tab_id: "", tab_label: rec.title, cwd: rec.work_dir ?? "", agent: req.agent,
                        agent_status: "working", has_agent: true, focused: false))
            } catch {
                self.error = "Could not create it: \(error)"
            }
        }
    }
}

/// The fixtures' source: fixed answers, and a create that is recorded only.
@MainActor
public final class FixtureNewAgentSource: NewAgentSource {
    public private(set) var created: [CreateAgent] = []
    public init() {}

    public func hosts() async -> [String] { ["local", "minime"] }

    public func config(host: String) async throws -> AgentConfig {
        AgentConfig(
            branch_prefix: "lasso/", default_agent: "claude", last_repo: "/home/stephan/projects/exa/lasso",
            last_agent: "claude", last_agent_type: "git",
            harnesses: [
                .init(id: "claude", label: "Claude Code", supports_plan_mode: true, effort_levels: ["low", "medium", "high", "max"], model_suggestions: ["opus", "sonnet"]),
                .init(id: "omp", label: "omp", supports_plan_mode: true, effort_levels: ["low", "medium", "high"], model_suggestions: []),
            ])
    }

    public func repos(host: String) async throws -> [RepoEntry] {
        [
            RepoEntry(path: "/home/stephan/projects/exa/lasso", name: "lasso", last_base_branch: "main"),
            RepoEntry(path: "/home/stephan/projects/exa/isb", name: "isb", last_base_branch: "main"),
        ]
    }

    public func branches(repo: String, host: String) async throws -> RepoBranches {
        RepoBranches(branches: ["main", "lasso-desktop-mac"], default: "main")
    }

    public func create(_ req: CreateAgent) async throws -> CreatedAgent {
        created.append(req)
        return CreatedAgent(id: "a1", title: "fixture", agent: req.agent, work_dir: "/tmp", workspace_id: "w9", root_pane: "w9:p1")
    }
}
