import AppKit
import SwiftUI

/// Colour tokens from the approved mock. Each resolves per appearance, so the window follows the system theme.
enum DS {
    private static func dynamic(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            let hex = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light
            return NSColor(srgbRed: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255,
                           blue: CGFloat(hex & 0xff) / 255, alpha: 1)
        })
    }
    static let panel = dynamic(0xffffff, 0x0b0c0d)
    static let raise = dynamic(0xeef0f1, 0x17191b)
    static let fg = dynamic(0x17191b, 0xe9eaeb)
    static let dim = dynamic(0x5e6367, 0x9b9fa3)
    static let faint = dynamic(0x8b9094, 0x63676b)
    static let line = dynamic(0xe2e4e6, 0x26282a)
    static let track = dynamic(0xe7e9eb, 0x1c1e20)
    static let tab = dynamic(0x0a6cff, 0x0a6cff)
    static let bar = dynamic(0x2f7f87, 0x3d8f97)
    static let barSoft = dynamic(0x8dbcc1, 0x2b5f65)
    static let bad = dynamic(0xd4343a, 0xe5484d)
    static let warn = dynamic(0xb86a12, 0xf0a24b)

    static let h2 = Font.system(size: 16, weight: .semibold)
    static let big = Font.system(size: 18, weight: .semibold)
    static let sub = Font.system(size: 13)
    static let small = Font.system(size: 12.5)
    static let tiny = Font.system(size: 10.5)
}

/// The one tone-to-colour mapping for pipelines, stages and jobs. Finished uses the neutral teal bar, never green.
enum PipelineTone {
    case finished, waiting, running, attention, failed, pending, notReported
    var color: Color {
        switch self {
        case .finished: return DS.bar
        case .waiting: return DS.barSoft
        case .running: return DS.tab
        case .attention: return DS.warn
        case .failed: return DS.bad
        case .pending: return DS.track
        case .notReported: return DS.faint
        }
    }
    /// Text colour for state words: only failure and attention stand out.
    var textColor: Color { self == .failed ? DS.bad : self == .attention ? DS.warn : DS.dim }
}

/// A padded block with a hairline below, like the mock's `section`.
struct DSection<Content: View>: View {
    var rule = true
    @ViewBuilder let content: Content
    var body: some View {
        VStack(alignment: .leading, spacing: 0) { content }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 14)
            .overlay(alignment: .bottom) { if rule { DS.line.frame(height: 1) } }
    }
}

/// Title on the left, dim text on the right, baseline aligned.
struct DHeader<Trailing: View>: View {
    let title: Text
    @ViewBuilder let trailing: Trailing
    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 12) {
            title.font(DS.h2).lineLimit(2)
            Spacer(minLength: 8)
            trailing.font(DS.sub).foregroundStyle(DS.dim)
        }
    }
}

/// Small dim list label, as in the mock's `.lbl`.
struct DLabel: View {
    let text: String
    var trailing: String?
    var top: CGFloat = 14
    var body: some View {
        HStack {
            Text(text)
            Spacer()
            if let trailing { Text(trailing) }
        }.font(DS.small).foregroundStyle(DS.dim).padding(.top, top).padding(.bottom, 2)
    }
}

struct TabPill: View {
    let title: String
    let count: String?
    let selected: Bool
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            HStack(spacing: 7) {
                Text(title).fontWeight(.medium)
                if let count { Text(count).font(.system(size: 12)).opacity(0.75).monospacedDigit() }
            }
            .padding(.horizontal, 16).padding(.vertical, 7)
            .foregroundStyle(selected ? Color.white : hovering ? DS.fg : DS.dim)
            .background(selected ? DS.tab : Color.clear, in: RoundedRectangle(cornerRadius: 10))
            .overlay(alignment: .bottom) {
                if selected { Capsule().fill(Color.white.opacity(0.45)).frame(height: 2).padding(.horizontal, 14).padding(.bottom, 3) }
            }
            .contentShape(RoundedRectangle(cornerRadius: 10))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

struct Chip: View {
    let title: String
    let selected: Bool
    let action: () -> Void
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            Text(title).font(.system(size: 13)).lineLimit(1)
                .padding(.horizontal, 11).padding(.vertical, 4)
                .foregroundStyle(selected ? Color.white : hovering ? DS.fg : DS.dim)
                .background(selected ? DS.tab : Color.clear, in: RoundedRectangle(cornerRadius: 8))
                .contentShape(RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A flat raised button, as in the mock's `.btn`.
struct DButtonStyle: ButtonStyle {
    var role: ButtonRole?
    @Environment(\.isEnabled) private var enabled
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.font(.system(size: 13))
            .foregroundStyle(role == .destructive ? DS.bad : DS.fg)
            .padding(.horizontal, 11).padding(.vertical, 4)
            .background(DS.raise.opacity(configuration.isPressed ? 0.6 : 1), in: RoundedRectangle(cornerRadius: 8))
            .opacity(enabled ? 1 : 0.45)
    }
}

struct Pill: View {
    let text: String
    var on = false
    var body: some View {
        Text(text).font(.system(size: 12)).foregroundStyle(on ? DS.bar : DS.dim)
            .padding(.horizontal, 8).padding(.vertical, 1)
            .background(DS.raise, in: RoundedRectangle(cornerRadius: 7))
            .fixedSize()
            .accessibilityLabel("\(text)：\(on ? "开启" : "关闭")")
    }
}

/// A row-shaped button with hover and selected backgrounds, radius 9.
struct RowButton<Content: View>: View {
    var selected = false
    let action: () -> Void
    @ViewBuilder let content: Content
    @State private var hovering = false
    var body: some View {
        Button(action: action) {
            content
                .padding(.horizontal, 10).padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(selected || hovering ? DS.raise : Color.clear, in: RoundedRectangle(cornerRadius: 9))
                .contentShape(RoundedRectangle(cornerRadius: 9))
        }
        .buttonStyle(.plain).onHover { hovering = $0 }
        .padding(.horizontal, -10)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// One segment of a track; `dashed` draws the "not reported" pattern.
struct TrackSegment: View {
    let color: Color
    var dashed = false
    var body: some View {
        if dashed {
            GeometryReader { proxy in
                Path { path in
                    path.move(to: CGPoint(x: 0, y: proxy.size.height / 2))
                    path.addLine(to: CGPoint(x: proxy.size.width, y: proxy.size.height / 2))
                }.stroke(color, style: StrokeStyle(lineWidth: proxy.size.height, dash: [2, 2]))
            }
        } else {
            RoundedRectangle(cornerRadius: 2).fill(color)
        }
    }
}

/// One fact: small dim label over a semibold value, like the mock's `.k` / `.v`.
struct Fact: View {
    let label: String
    let value: AnyView
    init(_ label: String, _ text: String) { self.label = label; value = AnyView(Text(text)) }
    init<V: View>(_ label: String, @ViewBuilder value: () -> V) { self.label = label; self.value = AnyView(value()) }
    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).font(DS.small).foregroundStyle(DS.dim)
            value.font(.system(size: 15, weight: .semibold)).monospacedDigit().textSelection(.enabled).lineLimit(2)
        }.frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct DashboardEmptyState: View {
    let text: String
    var body: some View {
        Text(text).font(DS.sub).foregroundStyle(DS.dim).frame(maxWidth: .infinity).padding(.vertical, 24)
    }
}
