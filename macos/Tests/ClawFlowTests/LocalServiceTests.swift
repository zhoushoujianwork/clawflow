import XCTest
@testable import ClawFlow

final class LocalServiceTests: XCTestCase {
    func testStatusIgnoresNestedProcessesAndSupportsStoppedService() {
        var service = LocalService(id: "com.zhoushoujianwork.clawflow.controller", path: "/tmp/controller.plist")
        service.apply("gui/501/controller = {\n state = waiting\n child = {\n state = running\n pid = 99\n }\n}")
        XCTAssertTrue(service.loaded)
        XCTAssertFalse(service.running)
        XCTAssertNil(service.pid)
        service.apply("gui/501/controller = {\n state = running\n pid = 42\n}")
        XCTAssertTrue(service.running)
        XCTAssertEqual(service.pid, "42")
    }
    func testStartStopRestartTargetOnlySelectedLocalService() {
        let label = "com.zhoushoujianwork.clawflow.worker-test"
        var service = LocalService(id: label, path: "/tmp/worker with space.plist")
        XCTAssertEqual(service.arguments(for: "start", uid: 501), ["bootstrap", "gui/501", service.path])
        service.loaded = true
        XCTAssertEqual(service.arguments(for: "restart", uid: 501), ["kickstart", "-k", "gui/501/" + label])
        XCTAssertEqual(service.arguments(for: "stop", uid: 501), ["bootout", "gui/501/" + label])
        XCTAssertFalse(LocalService.accepts("com.patsnap.relayer.worker"))
        XCTAssertFalse(LocalService.accepts("com.zhoushoujianwork.clawflow.web"))
    }
}
