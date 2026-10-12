import Combine
import Foundation

// The New form: the web app's New dialog (⌘O an agent, ⌘I a terminal;
// src/web/src/components/NewDialog.tsx and NewTerminalForm.tsx) over the same
// routes. An agent in a repository or a scratch one ends in its own window; a
// terminal ends in the herdr window, on its pane.

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
    /// The workspace a new terminal goes in, by label ("" or absent: Scratch).
    public var default_terminal_workspace: String?
}

/// One live herdr workspace (GET /api/workspaces): where a terminal can go.
public struct TerminalWorkspace: Codable, Sendable, Equatable {
    public var workspace_id: String
    public var label: String
    public var tab_count: Int

    public init(workspace_id: String, label: String, tab_count: Int) {
        self.workspace_id = workspace_id
        self.label = label
        self.tab_count = tab_count
    }
}

struct WorkspacesPayload: Codable, Sendable { var workspaces: [TerminalWorkspace] }

/// POST /api/create-terminal's body (src/terminal_create.go createTerminalReq),
/// as the web's terminal form sends it.
public struct CreateTerminal: Codable, Sendable, Equatable {
    public var host: String
    /// "" opens an interactive shell; several lines run as one script.
    public var command: String
    public var cwd: String?
    public var workspace_id: String?
    public var workspace_name: String
    public var tab_name: String?
    public var focus = true
}

/// What /api/create-terminal answers: the new terminal's pane, and what did
/// not take (the terminal exists either way).
public struct CreatedTerminal: Codable, Sendable, Equatable {
    public var workspace_id: String
    public var tab_id: String?
    public var root_pane: String
    public var command_error: String?
    public var tab_name_error: String?
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
    /// The machines to run on: lasso's value for each, and its name.
    func hosts() async -> [NewAgentModel.Choice]
    func config(host: String) async throws -> AgentConfig
    func repos(host: String) async throws -> [RepoEntry]
    func branches(repo: String, host: String) async throws -> RepoBranches
    func create(_ req: CreateAgent) async throws -> CreatedAgent
    /// The host's live workspaces, for where a terminal goes.
    func workspaces(host: String) async throws -> [TerminalWorkspace]
    func createTerminal(_ req: CreateTerminal) async throws -> CreatedTerminal
    /// Upload a file pasted or dropped on the prompt to `host`'s machine;
    /// the path it landed at there.
    func upload(_ file: URL, host: String) async throws -> String
}

@MainActor
public final class LiveNewAgentSource: NewAgentSource {
    let client: LassoClient
    public init(client: LassoClient) { self.client = client }

    /// lasso's own machine, then every host it can drive right now. Each by
    /// its name: "local" means lasso's machine, which is rarely this Mac.
    public func hosts() async -> [NewAgentModel.Choice] {
        guard let h = try? await client.get("/api/hosts", as: HostsPayload.self) else {
            return [.init(value: "local", title: "lasso's machine")]
        }
        return [.init(value: "local", title: h.local.hostname)]
            + h.hosts.filter { $0.reachable && $0.running && $0.compatible }.map { .init(value: $0.alias, title: $0.alias) }
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

    public func workspaces(host: String) async throws -> [TerminalWorkspace] {
        try await client.get("/api/workspaces", query: ["host": host], as: WorkspacesPayload.self).workspaces
    }

    public func createTerminal(_ req: CreateTerminal) async throws -> CreatedTerminal {
        try await client.post("/api/create-terminal", json: req)
    }

    public func upload(_ file: URL, host: String) async throws -> String {
        try await client.pasteFile(file, host: host)
    }
}

/// The New Agent form's state.
@MainActor
public final class NewAgentModel: ObservableObject {
    public enum Kind: String, CaseIterable, Sendable {
        case git, scratch, terminal
        public var label: String {
            switch self {
            case .git: "In a repository"
            case .scratch: "Scratch"
            case .terminal: "Terminal"
            }
        }
    }

    /// The web's terminal form caps a command at this many characters.
    static let maxCommand = 512
    /// The workspace a terminal goes in when the lasso names none (the web's SCRATCH_WORKSPACE).
    static let scratchWorkspace = "Scratch"

    @Published public var kind: Kind
    @Published public var host = "local" { didSet { if host != oldValue { Task { await loadHost() } } } }
    @Published public var repo = "" { didSet { if repo != oldValue { Task { await loadBranches() } } } }
    @Published public var baseBranch = ""
    @Published public var agent = ""
    @Published public var model = ""
    @Published public var effort = ""
    @Published public var planMode = false
    /// Terminal: the directory its shell starts in ("" is the workspace's default).
    @Published public var cwd = ""
    /// Terminal: the workspace it goes in, from the host's setup.
    private var terminalWorkspace = NewAgentModel.scratchWorkspace
    /// Typing in the prompt clears the last paste's notice.
    @Published public var prompt = "" { didSet { if !attaching, prompt != oldValue { notice = nil } } }
    @Published public private(set) var hostChoices: [Choice] = []
    public var hosts: [String] { hostChoices.map(\.value) }
    /// The machine's name, never "local".
    public var hostName: String { hostChoices.first { $0.value == host }?.title ?? (host == "local" ? "lasso's machine" : host) }
    @Published public private(set) var harnesses: [AgentConfig.Harness] = []
    @Published public private(set) var repos: [RepoEntry] = []
    @Published public private(set) var branches: [String] = []
    @Published public private(set) var busy = false
    @Published public private(set) var error: String?
    /// A file pasted or dropped on the prompt is uploading.
    @Published public private(set) var attaching = false
    /// How the last paste or drop went (attached, refused).
    @Published public private(set) var notice: String?

    /// Which chip's picker is open, its filter text, and the row the arrow
    /// keys are on (an index into `matches`).
    @Published public var openChip: String? {
        didSet {
            guard openChip != oldValue else { return }
            chipFilter = ""
            cursor = openChip.map { c in matches.firstIndex { $0.value == selected(c) } ?? 0 } ?? 0
        }
    }
    @Published public var chipFilter = "" { didSet { if chipFilter != oldValue { cursor = 0 } } }
    @Published public var cursor = 0
    /// The prompt has the cursor (its frame lights up).
    @Published public var promptFocused = false
    let source: NewAgentSource
    /// The header was dragged (the controller moves the panel).
    public var onDrag: (_ ended: Bool) -> Void = { _ in }
    /// The agent was created: open its window.
    public var onCreated: (HostPane) -> Void = { _ in }
    /// The terminal was created: show its pane in the herdr window, with
    /// what did not take (its command, its tab's name), if anything.
    public var onTerminal: (HostPane, String?) -> Void = { _, _ in }
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
        if kind == .terminal { return !busy && !attaching && commandFits }
        return !busy && !attaching && !title.isEmpty && !agent.isEmpty && (kind == .scratch || !repo.isEmpty)
    }

    /// The command is within the web form's limit.
    public var commandFits: Bool { prompt.count <= Self.maxCommand }

    public func load() async {
        hostChoices = await source.hosts()
        await loadHost()
    }

    func loadHost() async {
        do {
            let cfg = try await source.config(host: host)
            harnesses = cfg.harnesses ?? []
            let w = cfg.default_terminal_workspace?.trimmingCharacters(in: .whitespaces) ?? ""
            terminalWorkspace = w.isEmpty ? Self.scratchWorkspace : w
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

    // MARK: the chips' pickers

    /// One choice in a chip's picker.
    public struct Choice: Identifiable, Equatable, Sendable {
        public var value: String
        public var title: String
        public var detail: String?
        public var id: String { value }
    }

    /// What a chip's picker offers.
    public func choices(_ chip: String) -> [Choice] {
        switch chip {
        case "repo":
            return repos.map { Choice(value: $0.path, title: $0.name, detail: ($0.path as NSString).abbreviatingWithTildeInPath) }
        case "branch": return branches.map { Choice(value: $0, title: $0) }
        case "host": return hostChoices
        case "agent": return harnesses.map { Choice(value: $0.id, title: $0.label) }
        case "model":
            // Suggestions are a hint, not a list to stay inside (the web's
            // EditableCombobox): a model picked by hand stays listed.
            var names = harness?.model_suggestions ?? []
            if !model.isEmpty, !names.contains(model) { names.insert(model, at: 0) }
            return [Choice(value: "", title: "Default")] + names.map { Choice(value: $0, title: $0) }
        case "effort":
            return [Choice(value: "", title: "Default")] + (harness?.effort_levels ?? []).map { Choice(value: $0, title: $0.capitalized) }
        default: return []
        }
    }

    public func selected(_ chip: String) -> String {
        switch chip {
        case "repo": repo
        case "branch": baseBranch
        case "host": host
        case "agent": agent
        case "model": model
        case "effort": effort
        default: ""
        }
    }

    /// Long lists get a filter: repositories, branches, a big fleet; and the
    /// model, which takes any name.
    public func searchable(_ chip: String) -> Bool {
        chip == "repo" || chip == "branch" || chip == "model" || (chip == "host" && hosts.count > 8)
    }

    /// The open picker's rows, filtered. In the model's, a typed name no
    /// suggestion has comes first, to use as it is.
    public var matches: [Choice] {
        guard let c = openChip else { return [] }
        let typed = chipFilter.trimmingCharacters(in: .whitespaces)
        let q = typed.lowercased()
        var rows = choices(c).filter { q.isEmpty || $0.title.lowercased().contains(q) || ($0.detail ?? "").lowercased().contains(q) }
        if c == "model", !typed.isEmpty, !rows.contains(where: { $0.value.lowercased() == q }) {
            rows.insert(Choice(value: typed, title: "Use \u{201C}\(typed)\u{201D}"), at: 0)
        }
        return Array(rows.prefix(80))
    }

    /// ↑/↓ in an open picker.
    public func moveCursor(_ by: Int) {
        let n = matches.count
        guard n > 0 else { return }
        cursor = min(max(cursor + by, 0), n - 1)
    }

    /// ⏎ in an open picker: the row the cursor is on.
    public func chooseCursor() {
        let m = matches
        guard let c = openChip, m.indices.contains(cursor) else { return }
        choose(c, m[cursor].value)
    }

    public func choose(_ chip: String, _ value: String) {
        switch chip {
        case "repo": repo = value
        case "branch": baseBranch = value
        case "host": host = value
        case "agent":
            if agent != value {
                agent = value
                model = ""
                effort = ""
            }
        case "model": model = value
        case "effort": effort = value
        default: break
        }
        openChip = nil
    }

    /// The body for /api/create-agent.
    public var request: CreateAgent {
        CreateAgent(
            host: host, type: kind.rawValue, prompt: prompt.trimmingCharacters(in: .whitespacesAndNewlines),
            repo: kind == .git ? repo : nil, base_branch: kind == .git && !baseBranch.isEmpty ? baseBranch : nil,
            agent: agent, model: model.isEmpty ? nil : model, effort: effort.isEmpty ? nil : effort,
            plan_mode: planMode && (harness?.supports_plan_mode ?? false))
    }

    /// The body for /api/create-terminal, as the web's terminal form builds
    /// it: the host's terminal workspace (by id when it is live, else by
    /// name, which creates it; Scratch is found or made by lasso), a bare
    /// shell's tab numbered after the workspace's last, and a command's left
    /// for lasso to name.
    public func terminalRequest(workspaces: [TerminalWorkspace]) -> CreateTerminal {
        let match = workspaces.first { $0.label == terminalWorkspace }
        let blank = prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let dir = cwd.trimmingCharacters(in: .whitespaces)
        return CreateTerminal(
            host: host, command: blank ? "" : prompt, cwd: dir.isEmpty ? nil : dir, workspace_id: match?.workspace_id,
            workspace_name: terminalWorkspace, tab_name: blank ? String((match?.tab_count ?? 0) + 1) : nil)
    }

    /// Terminal mode's Run: create it, then show its pane in the herdr window.
    private func run() {
        busy = true
        error = nil
        let host = host
        Task {
            defer { busy = false }
            do {
                // Without the list, Scratch still works: lasso finds or makes it.
                let live = (try? await source.workspaces(host: host)) ?? []
                let req = terminalRequest(workspaces: live)
                let t = try await source.createTerminal(req)
                guard !t.root_pane.isEmpty else {
                    error = "lasso created the terminal but named no pane for it"
                    return
                }
                let warnings = [
                    t.command_error.map { "The command was not typed in: \($0)" },
                    t.tab_name_error.map { "Its tab could not be named: \($0)" },
                ].compactMap { $0 }
                let name = req.tab_name ?? req.command.split(separator: "\n").first.map(String.init) ?? "terminal"
                onTerminal(
                    HostPane(
                        host: host, host_label: hostName, pane_id: t.root_pane, workspace_id: t.workspace_id,
                        workspace_label: req.workspace_name, tab_id: t.tab_id ?? "", tab_label: name, cwd: req.cwd ?? "", agent: "",
                        agent_status: "idle", has_agent: false, focused: true),
                    warnings.isEmpty ? nil : warnings.joined(separator: " "))
            } catch {
                self.error = "Could not start the terminal: \(error)"
            }
        }
    }

    /// Create it, then open its window on the pane it boots in (an agent),
    /// or show it in the herdr window (a terminal).
    public func create() {
        guard canCreate else { return }
        if kind == .terminal { return run() }
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

extension NewAgentModel: AttachingModel {
    /// Files and images pasted or dropped on the prompt: uploaded to the
    /// chosen machine (lasso's paste-file route, the composer's), their paths
    /// put in the prompt at `caret`. The agent does not exist yet, so they
    /// go where it will run; pick the machine first.
    public func attach(_ files: [URL], at caret: Int?) {
        guard !files.isEmpty else { return }
        guard !attaching else {
            files.forEach(Attachments.discardIfScratch)
            notice = "Wait a moment: one upload at a time"
            return
        }
        attaching = true
        error = nil
        notice = Attachments.progress(files)
        let host = host
        Task {
            defer { attaching = false }
            do {
                notice = try await Attachments.upload(files, at: caret, into: \.prompt, of: self) {
                    try await self.source.upload($0, host: host)
                }
            } catch {
                notice = nil
                self.error = "Attach failed: \(error)"
            }
        }
    }

    public func say(_ s: String?) { notice = s }
}

/// The fixtures' source: fixed answers, and a create that is recorded only.
@MainActor
public final class FixtureNewAgentSource: NewAgentSource {
    public private(set) var created: [CreateAgent] = []
    public init() {}

    public func hosts() async -> [NewAgentModel.Choice] {
        [.init(value: "local", title: "titan"), .init(value: "minime", title: "minime")]
    }

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

    public private(set) var uploads: [String] = []

    public func upload(_ file: URL, host: String) async throws -> String {
        uploads.append("\(host):\(file.lastPathComponent)")
        return "/fixture/\(host)/" + file.lastPathComponent
    }

    public func create(_ req: CreateAgent) async throws -> CreatedAgent {
        created.append(req)
        return CreatedAgent(id: "a1", title: "fixture", agent: req.agent, work_dir: "/tmp", workspace_id: "w9", root_pane: "w9:p1")
    }

    public private(set) var terminals: [CreateTerminal] = []
    /// What create-terminal answers next (a test sets a failure or a warning).
    public var terminalAnswer: Result<CreatedTerminal, LassoError> = .success(
        CreatedTerminal(workspace_id: "w3", tab_id: "w3:t4", root_pane: "w3:p7"))

    public func workspaces(host: String) async throws -> [TerminalWorkspace] {
        [TerminalWorkspace(workspace_id: "w1", label: "lasso", tab_count: 2), TerminalWorkspace(workspace_id: "w3", label: "Scratch", tab_count: 3)]
    }

    public func createTerminal(_ req: CreateTerminal) async throws -> CreatedTerminal {
        terminals.append(req)
        return try terminalAnswer.get()
    }
}

/// The New form's prompt, typed and not yet sent: closing the form keeps it,
/// the next form (after a relaunch or an update too) starts with it, and
/// creating the agent or terminal clears it. One per Mac, like the web's
/// drafts are per browser.
@MainActor
public enum NewAgentDraft {
    static let key = "newAgentDraft"
    static var defaults = UserDefaults.standard

    public static var text: String {
        get { defaults.string(forKey: key) ?? "" }
        set { newValue.isEmpty ? defaults.removeObject(forKey: key) : defaults.set(newValue, forKey: key) }
    }
}
