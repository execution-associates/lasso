import Foundation

/// A damped harmonic oscillator in SwiftUI's response/damping terms: the
/// physical spring the spec describes, against which both SwiftUI's own
/// Spring and the frames the probe recorded are checked.
public struct SpringModel: Sendable, Equatable {
    public var response: Double
    public var damping: Double

    public init(response: Double, damping: Double) {
        self.response = response
        self.damping = damping
    }

    /// Fraction of the way from start to target at time t (step response).
    public func progress(_ t: Double) -> Double {
        guard t > 0 else { return 0 }
        let w0 = 2 * Double.pi / response
        let z = damping
        if z < 1 {
            let wd = w0 * sqrt(1 - z * z)
            return 1 - exp(-z * w0 * t) * (cos(wd * t) + (z * w0 / wd) * sin(wd * t))
        } else if z == 1 {
            return 1 - exp(-w0 * t) * (1 + w0 * t)
        } else {
            let s = sqrt(z * z - 1)
            let r1 = -w0 * (z - s), r2 = -w0 * (z + s)
            return 1 - (r2 * exp(r1 * t) - r1 * exp(r2 * t)) / (r2 - r1)
        }
    }

    /// Peak overshoot past the target, as a fraction of travel (closed form).
    public var overshootFraction: Double {
        damping < 1 ? exp(-damping * Double.pi / sqrt(1 - damping * damping)) : 0
    }

    /// The last time the motion is more than `tolerance` (same unit as
    /// travel) away from the target: when it has settled.
    public func settleTime(travel: Double, tolerance: Double, step: Double = 0.0001) -> Double {
        var last = 0.0
        var t = 0.0
        while t < 3 {
            if abs(1 - progress(t)) * travel > tolerance { last = t }
            t += step
        }
        return last
    }
}
