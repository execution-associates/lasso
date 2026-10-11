import AppKit
import CoreText

/// The Execution Associates faces shipped in Contents/Resources/Fonts
/// (Geist, Geist Mono, Archivo; SIL OFL 1.1), registered for this process
/// only. Font tokens name them by family; a face that fails to register
/// falls back to the system font, and parity reports it.
public enum BundledFonts {
    public static let families = ["Geist", "Geist Mono", "Archivo"]

    @discardableResult
    public static func register(from dir: URL? = nil) -> [String] {
        let folder = dir ?? Bundle.main.resourceURL?.appendingPathComponent("Fonts")
        guard let folder,
              let files = try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)
        else { return [] }
        var registered: [String] = []
        for url in files where url.pathExtension.lowercased() == "ttf" {
            var error: Unmanaged<CFError>?
            if CTFontManagerRegisterFontsForURL(url as CFURL, .process, &error) {
                registered.append(url.lastPathComponent)
            } else if let e = error?.takeRetainedValue(),
                      CFErrorGetCode(e) == CTFontManagerError.alreadyRegistered.rawValue
            {
                registered.append(url.lastPathComponent)
            }
        }
        return registered
    }
}
