// The process the tests run in (Scripts/test.sh), in place of
// swiftpm-testing-helper. Two things make it the app's kind of process:
//
//   - It is linked as the app is, against the same SDK version (test.sh
//     builds it so). AppKit and SwiftUI pick some behaviour by the SDK the
//     executable was linked against, and the helper's is newer: under it a
//     hidden NSHostingView takes no clicks, under the app's it still does,
//     so a test passed there while the app's Browser window ignored every
//     click.
//   - Its main thread runs NSApp.run, as the app's does, so what the window
//     server sends (a real click, a key) reaches the windows. The test
//     bundle's own main runs on another thread: its async main waits there
//     (a run loop kept alive by a port), its tests run on the main actor,
//     whose queue NSApp.run's loop serves, and it exits the process when
//     they are done.
//
//   test-host <bundle executable> [swift-testing arguments...]
import AppKit

let args = CommandLine.arguments
guard args.count >= 2 else {
    FileHandle.standardError.write(Data("usage: test-host <bundle executable> [arguments...]\n".utf8))
    exit(2)
}
guard let bundle = dlopen(args[1], RTLD_NOW) else {
    FileHandle.standardError.write(Data("test-host: \(String(cString: dlerror()))\n".utf8))
    exit(1)
}
guard let entry = dlsym(bundle, "main") else {
    FileHandle.standardError.write(Data("test-host: the bundle has no main\n".utf8))
    exit(1)
}
typealias Main = @convention(c) (Int32, UnsafeMutablePointer<UnsafeMutablePointer<CChar>?>) -> Int32
let run = unsafeBitCast(entry, to: Main.self)
let argc = Int32(args.count - 1)
nonisolated(unsafe) let argv = UnsafeMutablePointer<UnsafeMutablePointer<CChar>?>.allocate(capacity: args.count)
for (i, a) in args.dropFirst().enumerated() { argv[i] = strdup(a) }
argv[args.count - 1] = nil

let app = NSApplication.shared
Thread.detachNewThread {
    RunLoop.current.add(Port(), forMode: .default)
    exit(run(argc, argv))
}
app.run()
