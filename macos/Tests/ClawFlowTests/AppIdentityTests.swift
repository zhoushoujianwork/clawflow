import XCTest
@testable import ClawFlow

final class AppIdentityTests: XCTestCase {
    private let stable = AppIdentity(bundleID: "com.clawflow.dashboard")
    private let dev = AppIdentity(bundleID: "com.clawflow.dashboard.dev.0123456789ab")
    private let malformed = AppIdentity(bundleID: "com.clawflow.dashboard.dev.XYZ")

    func testCandidateBundleIdentifierDiffersFromStable() {
        XCTAssertNotEqual(dev.bundleID, stable.bundleID)
        XCTAssertTrue(dev.isCandidate)
        XCTAssertEqual(dev.candidateID, "0123456789ab")
        XCTAssertFalse(stable.isCandidate)
        XCTAssertNil(stable.candidateID)
    }

    func testCandidateManagesOnlyItsOwnServices() {
        let own = "com.zhoushoujianwork.clawflow.dev.0123456789ab.controller"
        XCTAssertTrue(dev.manages(own))
        XCTAssertFalse(dev.manages("com.zhoushoujianwork.clawflow.dev.ffffffffffff.controller"))
        XCTAssertFalse(dev.manages("com.zhoushoujianwork.clawflow.controller"))
        XCTAssertFalse(dev.manages("com.zhoushoujianwork.clawflow.worker-main"))
        XCTAssertFalse(dev.manages("com.zhoushoujianwork.clawflow.postgres"))
    }

    func testStableNeverManagesCandidateServices() {
        XCTAssertTrue(stable.manages("com.zhoushoujianwork.clawflow.controller"))
        XCTAssertFalse(stable.manages("com.zhoushoujianwork.clawflow.dev.0123456789ab.controller"))
        XCTAssertFalse(LocalService.accepts("com.zhoushoujianwork.clawflow.dev.0123456789ab.worker-dev", identity: stable))
        XCTAssertTrue(LocalService.accepts("com.zhoushoujianwork.clawflow.dev.0123456789ab.worker-dev", identity: dev))
    }

    func testMalformedCandidateIdentityFailsClosed() {
        XCTAssertTrue(malformed.isCandidate)
        XCTAssertNil(malformed.candidateID)
        XCTAssertFalse(malformed.manages("com.zhoushoujianwork.clawflow.controller"))
        XCTAssertFalse(malformed.manages("com.zhoushoujianwork.clawflow.dev.0123456789ab.controller"))
        XCTAssertFalse(malformed.startsHostServices)
    }

    func testCandidateDoesNotStartHostServicesOrUseInstalledCLI() {
        XCTAssertFalse(dev.startsHostServices)
        XCTAssertFalse(dev.allowsInstalledCLIFallback)
        XCTAssertTrue(stable.startsHostServices)
        XCTAssertTrue(stable.allowsInstalledCLIFallback)
    }
}
