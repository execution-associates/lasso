// swift-tools-version:6.0
// LassoDesktop: the native macOS client for lasso. Builds with the Command
// Line Tools only (no Xcode), so no @State and no #Preview: view state lives
// in ObservableObjects. See PLAN.md. Sparkle (updates) is the one third-party
// package, and only the executable links it: the kit, the tests and the
// parity harness stay free of it.
import PackageDescription

let package = Package(
    name: "LassoDesktop",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "LassoDesktop", targets: ["LassoDesktop"])],
    dependencies: [.package(url: "https://github.com/sparkle-project/Sparkle.git", exact: "2.9.4")],
    targets: [
        .target(name: "LassoDesktopKit"),
        .executableTarget(
            name: "LassoDesktop",
            dependencies: ["LassoDesktopKit", .product(name: "Sparkle", package: "Sparkle")],
            linkerSettings: [.unsafeFlags(["-Xlinker", "-rpath", "-Xlinker", "@executable_path/../Frameworks"])]),
        .testTarget(name: "LassoDesktopTests", dependencies: ["LassoDesktopKit"]),
    ]
)
