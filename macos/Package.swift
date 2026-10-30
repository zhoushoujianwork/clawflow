// swift-tools-version: 5.9
import PackageDescription

let package = Package(name: "ClawFlow", platforms: [.macOS(.v14)], targets: [
    .executableTarget(name: "ClawFlow"),
    .testTarget(name: "ClawFlowTests", dependencies: ["ClawFlow"])
])
