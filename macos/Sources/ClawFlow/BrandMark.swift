import AppKit
import SwiftUI

enum BrandMark {
    /// The three original quadratic claw strokes from web/public/logo-100.svg.
    /// A template silhouette stays crisp at menu-bar size without the tile/glow.
    static let menuBar: NSImage = {
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { _ in
            NSColor.black.setStroke()
            for offset in [0.0, 7.0, 14.0] {
                let start = NSPoint(x: (6 + offset) * 0.6 - 0.6, y: 3)
                let control = NSPoint(x: (8 + offset) * 0.6 - 0.6, y: 9)
                let end = NSPoint(x: (12 + offset) * 0.6 - 0.6, y: 15)
                let path = NSBezierPath()
                path.move(to: start)
                path.curve(to: end,
                           controlPoint1: NSPoint(x: start.x + 2 * (control.x - start.x) / 3,
                                                  y: start.y + 2 * (control.y - start.y) / 3),
                           controlPoint2: NSPoint(x: end.x + 2 * (control.x - end.x) / 3,
                                                  y: end.y + 2 * (control.y - end.y) / 3))
                path.lineWidth = 1.8
                path.lineCapStyle = .round
                path.stroke()
            }
            return true
        }
        image.isTemplate = true
        return image
    }()
}

struct MenuBarBrand: View {
    let summary: StatusSummary
    var offloadAttention = false
    var body: some View {
        HStack(spacing: 3) {
            Image(nsImage: BrandMark.menuBar)
            if offloadAttention && summary.connected && summary.title != "Needs attention" {
                Image(systemName: "exclamationmark").font(.system(size: 9, weight: .semibold))
            } else if let symbol = summary.menuSymbol { Image(systemName: symbol).font(.system(size: 9, weight: .semibold)) }
            if let text = summary.menuText { Text(text).font(.system(size: 11, weight: .medium)).monospacedDigit() }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(offloadAttention ? "\(summary.accessibilityLabel). Offload needs setup" : summary.accessibilityLabel)
        .help(offloadAttention ? "ClawFlow · \(summary.title) · Offload needs setup" : "ClawFlow · \(summary.title)")
    }
}
