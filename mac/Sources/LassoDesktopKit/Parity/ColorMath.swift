import Foundation

/// CIE colour math for the parity harness: sRGB (D65) to CIELAB, and the
/// CIEDE2000 difference (Sharma, Wu, Dalal 2005).
public enum ColorMath {
    public struct Lab: Equatable, Sendable {
        public var l, a, b: Double
    }

    public static func lab(_ c: RGBA) -> Lab {
        func lin(_ u: Double) -> Double { u <= 0.04045 ? u / 12.92 : pow((u + 0.055) / 1.055, 2.4) }
        let r = lin(c.r), g = lin(c.g), b = lin(c.b)
        let x = (0.4124564 * r + 0.3575761 * g + 0.1804375 * b) / 0.95047
        let y = (0.2126729 * r + 0.7151522 * g + 0.0721750 * b) / 1.0
        let z = (0.0193339 * r + 0.1191920 * g + 0.9503041 * b) / 1.08883
        func f(_ t: Double) -> Double { t > 216.0 / 24389.0 ? cbrt(t) : (24389.0 / 27.0 * t + 16) / 116 }
        let fx = f(x), fy = f(y), fz = f(z)
        return Lab(l: 116 * fy - 16, a: 500 * (fx - fy), b: 200 * (fy - fz))
    }

    public static func deltaE2000(_ c1: RGBA, _ c2: RGBA) -> Double { deltaE2000(lab(c1), lab(c2)) }

    public static func deltaE2000(_ p: Lab, _ q: Lab) -> Double {
        let deg = Double.pi / 180
        let c1 = hypot(p.a, p.b), c2 = hypot(q.a, q.b)
        let cBar = (c1 + c2) / 2
        let g = 0.5 * (1 - sqrt(pow(cBar, 7) / (pow(cBar, 7) + pow(25, 7))))
        let a1 = (1 + g) * p.a, a2 = (1 + g) * q.a
        let cp1 = hypot(a1, p.b), cp2 = hypot(a2, q.b)
        func hue(_ b: Double, _ a: Double) -> Double {
            if a == 0 && b == 0 { return 0 }
            let h = atan2(b, a) / deg
            return h < 0 ? h + 360 : h
        }
        let h1 = hue(p.b, a1), h2 = hue(q.b, a2)
        let dL = q.l - p.l
        let dC = cp2 - cp1
        var dh: Double
        if cp1 * cp2 == 0 {
            dh = 0
        } else if abs(h2 - h1) <= 180 {
            dh = h2 - h1
        } else if h2 - h1 > 180 {
            dh = h2 - h1 - 360
        } else {
            dh = h2 - h1 + 360
        }
        let dH = 2 * sqrt(cp1 * cp2) * sin(dh / 2 * deg)
        let lBar = (p.l + q.l) / 2
        let cpBar = (cp1 + cp2) / 2
        var hBar: Double
        if cp1 * cp2 == 0 {
            hBar = h1 + h2
        } else if abs(h1 - h2) <= 180 {
            hBar = (h1 + h2) / 2
        } else if h1 + h2 < 360 {
            hBar = (h1 + h2 + 360) / 2
        } else {
            hBar = (h1 + h2 - 360) / 2
        }
        let t = 1 - 0.17 * cos((hBar - 30) * deg) + 0.24 * cos(2 * hBar * deg)
            + 0.32 * cos((3 * hBar + 6) * deg) - 0.20 * cos((4 * hBar - 63) * deg)
        let dTheta = 30 * exp(-pow((hBar - 275) / 25, 2))
        let rc = 2 * sqrt(pow(cpBar, 7) / (pow(cpBar, 7) + pow(25, 7)))
        let sl = 1 + 0.015 * pow(lBar - 50, 2) / sqrt(20 + pow(lBar - 50, 2))
        let sc = 1 + 0.045 * cpBar
        let sh = 1 + 0.015 * cpBar * t
        let rt = -sin(2 * dTheta * deg) * rc
        return sqrt(pow(dL / sl, 2) + pow(dC / sc, 2) + pow(dH / sh, 2) + rt * (dC / sc) * (dH / sh))
    }

    public static func hex(_ c: RGBA) -> String {
        func h(_ v: Double) -> String { String(format: "%02X", Int((max(0, min(1, v)) * 255).rounded())) }
        return "#" + h(c.r) + h(c.g) + h(c.b) + (c.a < 0.995 ? String(format: "@%.2f", c.a) : "")
    }
}
