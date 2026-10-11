// swift-tools-version:6.0
// LassoDesktop: the native macOS client for lasso. Builds with the Command
// Line Tools only (no Xcode), so no @State and no #Preview: view state lives
// in ObservableObjects. See PLAN.md.
import PackageDescription

let package = Package(
    name: "LassoDesktop",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "LassoDesktop", targets: ["LassoDesktop"])],
    targets: [
        .target(name: "LassoDesktopKit"),
        .executableTarget(name: "LassoDesktop", dependencies: ["LassoDesktopKit"]),
        .testTarget(name: "LassoDesktopTests", dependencies: ["LassoDesktopKit"]),
    ]
)
