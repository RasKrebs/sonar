import AppKit

// MARK: - JSON model matching `sonar list --json`

struct SonarPort: Decodable {
    let port: Int
    let pid: Int
    let process: String
    let command: String?
    let user: String?
    let bind_address: String?
    let type: String
    let url: String
    let cpu_percent: Double
    let memory_rss_bytes: Int64
    let thread_count: Int?
    let uptime: String?
    let state: String?
    let connections: Int
    let docker_container: String?
    let docker_image: String?
    let docker_compose_service: String?
    let docker_compose_project: String?
    let docker_container_port: Int?
}

// MARK: - App Delegate

class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private var menu: NSMenu!
    private var timer: Timer?
    private var ports: [SonarPort] = []
    private let sonarPath: String
    private var refreshing = false

    // Tags for items we update in-place
    private let tagHeader = 1000
    private let tagUpdated = 1001
    private let tagPortBase = 2000 // ports use tags 2000+

    override init() {
        let bundledPath = URL(fileURLWithPath: CommandLine.arguments[0])
            .deletingLastPathComponent()
            .appendingPathComponent("sonar")
            .path

        if FileManager.default.isExecutableFile(atPath: bundledPath) {
            sonarPath = bundledPath
        } else {
            sonarPath = AppDelegate.findInPath("sonar") ?? "sonar"
        }

        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        if let button = statusItem.button {
            if let image = NSImage(systemSymbolName: "dot.radiowaves.left.and.right",
                                   accessibilityDescription: "Sonar") {
                image.isTemplate = true
                let config = NSImage.SymbolConfiguration(pointSize: 14, weight: .medium)
                button.image = image.withSymbolConfiguration(config)
            } else {
                button.title = "S"
                button.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .bold)
            }
        }

        menu = NSMenu()
        menu.delegate = self
        statusItem.menu = menu

        // Build initial menu structure
        rebuildMenu()
        refresh()

        let t = Timer(timeInterval: 2.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
        RunLoop.main.add(t, forMode: .common)
        timer = t
    }

    // MARK: - Scanning

    private func refresh() {
        guard !refreshing else { return }
        refreshing = true
        DispatchQueue.global(qos: .utility).async { [weak self] in
            guard let self = self else { return }
            let result = self.runSonar(["list", "--json", "--stats"])
            let data = result.data(using: .utf8)
            let decoded = data.flatMap { try? JSONDecoder().decode([SonarPort].self, from: $0) } ?? []

            DispatchQueue.main.async {
                self.ports = decoded
                self.updateMenuItems()
                self.refreshing = false
            }
        }
    }

    // MARK: - Menu

    /// Full rebuild of menu structure — called once at startup and when port count changes.
    private func rebuildMenu() {
        menu.removeAllItems()

        // Header
        let headerItem = NSMenuItem(title: "Loading...", action: nil, keyEquivalent: "")
        headerItem.tag = tagHeader
        headerItem.isEnabled = false
        menu.addItem(headerItem)
        menu.addItem(NSMenuItem.separator())

        // Port items will be inserted here by updateMenuItems
        // (between separator after header and the footer items)

        // Footer
        let updatedItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
        updatedItem.tag = tagUpdated
        updatedItem.isEnabled = false
        menu.addItem(updatedItem)

        let refreshItem = NSMenuItem(title: "Refresh", action: #selector(refreshClicked), keyEquivalent: "r")
        refreshItem.target = self
        menu.addItem(refreshItem)

        let quitItem = NSMenuItem(title: "Quit Sonar Tray", action: #selector(quit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
    }

    /// Update menu items in-place so changes are visible while menu is open.
    private func updateMenuItems() {
        // Update header
        if let headerItem = menu.item(withTag: tagHeader) {
            let dockerCount = ports.filter { $0.type == "docker" }.count
            let userCount = ports.filter { $0.type == "user" }.count
            let systemCount = ports.filter { $0.type == "system" }.count

            var summary = "\(ports.count) ports"
            var parts: [String] = []
            if dockerCount > 0 { parts.append("\(dockerCount) docker") }
            if userCount > 0 { parts.append("\(userCount) user") }
            if systemCount > 0 { parts.append("\(systemCount) system") }
            if !parts.isEmpty { summary += " (\(parts.joined(separator: ", ")))" }

            headerItem.attributedTitle = NSAttributedString(string: summary, attributes: [
                .font: NSFont.monospacedSystemFont(ofSize: 12, weight: .semibold),
                .foregroundColor: NSColor.labelColor
            ])
        }

        // Update timestamp
        if let updatedItem = menu.item(withTag: tagUpdated) {
            let formatter = DateFormatter()
            formatter.dateFormat = "HH:mm:ss"
            updatedItem.attributedTitle = NSAttributedString(
                string: "Updated \(formatter.string(from: Date()))",
                attributes: [
                    .font: NSFont.monospacedSystemFont(ofSize: 10, weight: .regular),
                    .foregroundColor: NSColor.tertiaryLabelColor
                ])
        }

        // Find insertion range: after the first separator, before the updated-timestamp item
        let insertAfter = 2 // header + separator
        let insertBefore: Int
        if let updatedItem = menu.item(withTag: tagUpdated) {
            insertBefore = menu.index(of: updatedItem)
        } else {
            insertBefore = menu.numberOfItems - 3
        }

        // Remove old port items (everything between insertAfter and insertBefore)
        while insertBefore > insertAfter, menu.numberOfItems > insertAfter,
              menu.item(at: insertAfter)?.tag != tagUpdated {
            menu.removeItem(at: insertAfter)
        }

        // Insert new port items
        if ports.isEmpty {
            let emptyItem = NSMenuItem(title: "No active ports", action: nil, keyEquivalent: "")
            emptyItem.isEnabled = false
            menu.insertItem(emptyItem, at: insertAfter)
        } else {
            var idx = insertAfter
            let grouped = Dictionary(grouping: ports) { port -> String in
                if let project = port.docker_compose_project, !project.isEmpty {
                    return project
                }
                return ""
            }

            let projectNames = grouped.keys.filter { !$0.isEmpty }.sorted()
            let ungrouped = grouped[""] ?? []

            for project in projectNames {
                let projectItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
                projectItem.isEnabled = false
                projectItem.attributedTitle = NSAttributedString(string: "  \(project)", attributes: [
                    .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .semibold),
                    .foregroundColor: NSColor.secondaryLabelColor
                ])
                menu.insertItem(projectItem, at: idx); idx += 1

                for port in (grouped[project] ?? []).sorted(by: { $0.port < $1.port }) {
                    menu.insertItem(buildPortItem(port), at: idx); idx += 1
                }
                menu.insertItem(NSMenuItem.separator(), at: idx); idx += 1
            }

            if !ungrouped.isEmpty {
                if !projectNames.isEmpty {
                    let otherItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
                    otherItem.isEnabled = false
                    otherItem.attributedTitle = NSAttributedString(string: "  other", attributes: [
                        .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .semibold),
                        .foregroundColor: NSColor.secondaryLabelColor
                    ])
                    menu.insertItem(otherItem, at: idx); idx += 1
                }

                for port in ungrouped.sorted(by: { $0.port < $1.port }) {
                    menu.insertItem(buildPortItem(port), at: idx); idx += 1
                }
                menu.insertItem(NSMenuItem.separator(), at: idx); idx += 1
            }
        }
    }

    private func buildPortItem(_ port: SonarPort) -> NSMenuItem {
        let name = displayName(port)
        let mem = formatBytes(port.memory_rss_bytes)
        let cpu = String(format: "%.1f%%", port.cpu_percent)
        let paddedPort = "\(port.port)".padding(toLength: 5, withPad: " ", startingAt: 0)
        let paddedName = name.padding(toLength: 20, withPad: " ", startingAt: 0)
        let label = "\(paddedPort)  \(paddedName)  \(cpu) cpu  \(mem) mem"

        let portItem = NSMenuItem(title: label, action: nil, keyEquivalent: "")

        let typeIcon: String
        switch port.type {
        case "docker": typeIcon = "🐳"
        case "system": typeIcon = "⚙️"
        default: typeIcon = "▸"
        }

        portItem.attributedTitle = NSAttributedString(string: "\(typeIcon) \(label)", attributes: [
            .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        ])

        let submenu = NSMenu()

        addInfoItem(submenu, "Process", port.process)
        if let cmd = port.command, !cmd.isEmpty { addInfoItem(submenu, "Command", cmd) }
        if let user = port.user, !user.isEmpty { addInfoItem(submenu, "User", user) }
        addInfoItem(submenu, "PID", "\(port.pid)")
        if let bind = port.bind_address, !bind.isEmpty { addInfoItem(submenu, "Bind", bind) }
        if let state = port.state, !state.isEmpty { addInfoItem(submenu, "State", state) }
        if let uptime = port.uptime, !uptime.isEmpty { addInfoItem(submenu, "Uptime", uptime) }
        addInfoItem(submenu, "CPU", cpu)
        addInfoItem(submenu, "Memory", mem)
        if let threads = port.thread_count, threads > 0 { addInfoItem(submenu, "Threads", "\(threads)") }
        if port.connections > 0 { addInfoItem(submenu, "Connections", "\(port.connections)") }

        if let container = port.docker_container, !container.isEmpty {
            submenu.addItem(NSMenuItem.separator())
            addInfoItem(submenu, "Container", container)
            if let image = port.docker_image, !image.isEmpty { addInfoItem(submenu, "Image", image) }
            if let service = port.docker_compose_service, !service.isEmpty { addInfoItem(submenu, "Service", service) }
            if let cport = port.docker_container_port, cport > 0 { addInfoItem(submenu, "Container Port", "\(cport)") }
        }

        submenu.addItem(NSMenuItem.separator())

        let openItem = NSMenuItem(title: "Open in Browser", action: #selector(openPort(_:)), keyEquivalent: "")
        openItem.target = self
        openItem.representedObject = port
        submenu.addItem(openItem)

        let copyItem = NSMenuItem(title: "Copy URL", action: #selector(copyURL(_:)), keyEquivalent: "")
        copyItem.target = self
        copyItem.representedObject = port
        submenu.addItem(copyItem)

        submenu.addItem(NSMenuItem.separator())

        let killTitle = port.type == "docker" ? "Stop Container" : "Kill Process"
        let killItem = NSMenuItem(title: killTitle, action: #selector(killPort(_:)), keyEquivalent: "")
        killItem.target = self
        killItem.representedObject = port
        submenu.addItem(killItem)

        portItem.submenu = submenu
        return portItem
    }

    private func addInfoItem(_ menu: NSMenu, _ key: String, _ value: String) {
        let text = "\(key): \(value)"
        let item = NSMenuItem(title: text, action: nil, keyEquivalent: "")
        item.isEnabled = false
        item.attributedTitle = NSAttributedString(string: text, attributes: [
            .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular),
            .foregroundColor: NSColor.secondaryLabelColor
        ])
        menu.addItem(item)
    }

    // MARK: - Actions

    @objc private func openPort(_ sender: NSMenuItem) {
        guard let port = sender.representedObject as? SonarPort else { return }
        if let url = URL(string: port.url) {
            NSWorkspace.shared.open(url)
        }
    }

    @objc private func copyURL(_ sender: NSMenuItem) {
        guard let port = sender.representedObject as? SonarPort else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(port.url, forType: .string)
    }

    @objc private func killPort(_ sender: NSMenuItem) {
        guard let port = sender.representedObject as? SonarPort else { return }

        let name = displayName(port)
        let action = port.type == "docker" ? "stop" : "kill"

        let alert = NSAlert()
        alert.messageText = "\(action.capitalized) port \(port.port)?"
        alert.informativeText = "This will \(action) \(name) (pid \(port.pid))."
        alert.alertStyle = .warning
        alert.addButton(withTitle: action.capitalized)
        alert.addButton(withTitle: "Cancel")

        if alert.runModal() == .alertFirstButtonReturn {
            DispatchQueue.global(qos: .utility).async { [weak self] in
                _ = self?.runSonar(["kill", String(port.port)])
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
                    self?.refresh()
                }
            }
        }
    }

    @objc private func refreshClicked() {
        refresh()
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    // MARK: - Helpers

    private func displayName(_ port: SonarPort) -> String {
        if let service = port.docker_compose_service, !service.isEmpty {
            return service
        }
        if let container = port.docker_container, !container.isEmpty {
            return container
        }
        return port.process
    }

    private func formatBytes(_ bytes: Int64) -> String {
        if bytes < 1024 { return "\(bytes)B" }
        let kb = Double(bytes) / 1024
        if kb < 1024 { return String(format: "%.0fK", kb) }
        let mb = kb / 1024
        if mb < 1024 { return String(format: "%.1fM", mb) }
        let gb = mb / 1024
        return String(format: "%.1fG", gb)
    }

    private func runSonar(_ args: [String]) -> String {
        let proc = Process()
        let pipe = Pipe()
        proc.executableURL = URL(fileURLWithPath: sonarPath)
        proc.arguments = args
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice
        proc.environment = ProcessInfo.processInfo.environment

        do {
            try proc.run()
            proc.waitUntilExit()
        } catch {
            return ""
        }

        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        return String(data: data, encoding: .utf8) ?? ""
    }

    private static func findInPath(_ name: String) -> String? {
        guard let pathEnv = ProcessInfo.processInfo.environment["PATH"] else { return nil }
        for dir in pathEnv.split(separator: ":") {
            let full = "\(dir)/\(name)"
            if FileManager.default.isExecutableFile(atPath: full) {
                return full
            }
        }
        return nil
    }
}

// MARK: - Entry point

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
