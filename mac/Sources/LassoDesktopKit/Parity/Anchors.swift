import AppKit
import QuartzCore
import SwiftUI

/// Frames of named views, in the parity root's coordinate space. Views carry
/// `.parityAnchor("rail.inbox")`; outside parity mode the modifier is a no-op.
public struct ParityFramesKey: PreferenceKey {
    public static let defaultValue: [String: CGRect] = [:]

    public static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue()) { $1 }
    }
}

public enum ParityRoot {
    public static let space = "parity-root"
}

struct ParityEnabledKey: EnvironmentKey {
    static let defaultValue = false
}

struct RevealProbeKey: EnvironmentKey {
    static let defaultValue: MotionProbe? = nil
}

extension EnvironmentValues {
    public var parityEnabled: Bool {
        get { self[ParityEnabledKey.self] }
        set { self[ParityEnabledKey.self] = newValue }
    }

    /// Records every value SwiftUI interpolates for the rail's reveal.
    public var revealProbe: MotionProbe? {
        get { self[RevealProbeKey.self] }
        set { self[RevealProbeKey.self] = newValue }
    }
}

struct ParityAnchor: ViewModifier {
    let id: String
    @Environment(\.parityEnabled) private var enabled

    func body(content: Content) -> some View {
        if enabled {
            content.background(
                GeometryReader { g in
                    Color.clear.preference(
                        key: ParityFramesKey.self, value: [id: g.frame(in: .named(ParityRoot.space))])
                })
        } else {
            content
        }
    }
}

extension View {
    public func parityAnchor(_ id: String) -> some View { modifier(ParityAnchor(id: id)) }
}

/// Timestamped samples of one animated value, recorded from inside SwiftUI's
/// interpolation (see RevealHeight). Thread-safe: SwiftUI may set
/// animatableData off the main actor.
public final class MotionProbe: @unchecked Sendable {
    private let lock = NSLock()
    private var samples: [(t: Double, v: Double)] = []

    public init() {}

    public func record(_ v: Double) {
        let t = CACurrentMediaTime()
        lock.lock()
        samples.append((t, v))
        lock.unlock()
    }

    /// A sample whose time is already known (animation time).
    public func recordExact(t: Double, v: Double) {
        lock.lock()
        samples.append((t, v))
        lock.unlock()
    }

    public func reset() {
        lock.lock()
        samples.removeAll()
        lock.unlock()
    }

    public func snapshot() -> [(t: Double, v: Double)] {
        lock.lock()
        defer { lock.unlock() }
        return samples
    }
}

/// Wraps an animation and records, on SwiftUI's own animation clock, the
/// progress it returns for a motion of one magnitude. The harness wraps the
/// rail's token animation with it, so what it measures is the animation the
/// app's transaction actually ran.
public struct ProbedAnimation: CustomAnimation {
    let base: Animation
    let probe: MotionProbe
    let travel: Double

    public func animate<V: VectorArithmetic>(value: V, time: TimeInterval, context: inout AnimationContext<V>) -> V? {
        let out = base.animate(value: value, time: time, context: &context)
        let total = value.magnitudeSquared.squareRoot()
        if abs(total - travel) < 0.5, total > 0 {
            // As a fraction of this value's own change, scaled to travel, so
            // a colour that animates beside an opacity reads the same curve.
            let done = (out ?? value).magnitudeSquared.squareRoot()
            probe.recordExact(t: time, v: done / total * travel)
        }
        return out
    }

    public func shouldMerge<V: VectorArithmetic>(
        previous: Animation, value: V, time: TimeInterval, context: inout AnimationContext<V>
    ) -> Bool {
        base.shouldMerge(previous: previous, value: value, time: time, context: &context)
    }

    public static func == (a: ProbedAnimation, b: ProbedAnimation) -> Bool {
        a.base == b.base && a.probe === b.probe && a.travel == b.travel
    }

    public func hash(into h: inout Hasher) {
        h.combine(ObjectIdentifier(probe))
        h.combine(travel)
    }
}
