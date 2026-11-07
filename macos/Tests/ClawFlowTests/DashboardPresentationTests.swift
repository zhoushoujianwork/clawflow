import XCTest
@testable import ClawFlow

final class DashboardPresentationTests: XCTestCase {
    private func issue(_ state: String) throws -> IssueQueueRow {
        let json = #"{"key":"o/r#1","position":1,"number":1,"title":"t","tier":"P2","state":"\#(state)"}"#
        return try decoder().decode(IssueQueueRow.self, from: Data(json.utf8))
    }

    func testIssueDetailNeverShowsRawStateCodes() throws {
        for (state, expected) in [
            ("in_flight", nil),
            ("in_flight:no_verdict", "PR 已开，等待验收"),
            ("parked:acceptance_stale", "PR 已变更，验收已过期"),
            ("waiting:dependency", "等待依赖"),
            ("eligible:max_in_flight", "仓库名额已满"),
            ("eligible:area_busy", "和打开的 PR 改动范围重叠"),
            ("eligible:coordination checked", nil),
            // The controller's own sentence stays as received.
            ("waiting:coordination_waiting — Explicit dependency #669", "Explicit dependency #669"),
            // Unknown codes are dropped instead of shown raw.
            ("blocked:some_new_reason", nil),
        ] as [(String, String?)] {
            XCTAssertEqual(try issue(state).detail, expected, state)
        }
    }

    func testQueueStatusIsChinese() throws {
        XCTAssertEqual(try issue("in_flight:no_verdict").statusZh, "进行中")
        XCTAssertEqual(try issue("eligible").statusZh, "就绪，等待名额")
        XCTAssertEqual(try issue("blocked:x").statusZh, "已阻塞")
        XCTAssertEqual(try issue("parked:x").statusZh, "已搁置")
        XCTAssertEqual(try issue("eligible:coordination checked").statusZh, "协调已检查")
        XCTAssertEqual(try issue("something_new").statusZh, "状态未知")
    }

    private func snapshot(projects: String, tasks: String = "") throws -> DashboardSnapshot {
        let time = "2026-11-04T00:00:00Z"
        let json = #"{"schema_version":1,"version":"t","controller":"c","board":{"generated_at":"\#(time)","workers":[],"tasks":[\#(tasks)]},"projects":[\#(projects)]}"#
        return try DashboardSnapshot.decode(Data(json.utf8))
    }
    private func repo(_ name: String, enabled: Bool = true) -> String {
        #"{"name":"\#(name)","url":"https://example.test/\#(name)","platform":"github","enabled":\#(enabled),"ci_required":true,"acceptance_required":false,"auto_merge":false,"worker_tags":[],"issue_queue":[{"key":"\#(name)#1","position":1,"number":1,"tier":"P2","state":"in_flight"}]}"#
    }

    func testRepositoryInSeveralProjectsAppearsOnce() throws {
        let value = try snapshot(projects: #"{"name":"a","repos":[\#(repo("o/x")),\#(repo("o/y"))]},{"name":"b","repos":[\#(repo("o/y"))]},{"name":"c","repos":[\#(repo("o/z"))]}"#)
        XCTAssertEqual(value.uniqueProjects.map(\.name), ["a", "c"])
        XCTAssertEqual(value.uniqueProjects.flatMap(\.repos).map(\.name), ["o/x", "o/y", "o/z"])
        XCTAssertEqual(Destination.pipelines.badge(in: value), "3")
        XCTAssertEqual(Destination.projects.badge(in: value), "3")
        XCTAssertEqual(Destination.workers.badge(in: value), "0/0")
        XCTAssertNil(Destination.overview.badge(in: value))
        XCTAssertNil(Destination.pipelines.badge(in: nil))
    }

    func testRepoChipsIncludeEnabledAndActiveRepositories() throws {
        let idle = #"{"name":"o/idle","url":"https://example.test/o/idle","platform":"github","enabled":false,"ci_required":false,"acceptance_required":false,"auto_merge":false,"worker_tags":[]}"#
        let value = try snapshot(projects: #"{"name":"a","repos":[\#(repo("o/x")),\#(repo("o/paused", enabled: false)),\#(idle)]}"#)
        // o/paused is disabled but still has an active pipeline; o/idle has neither.
        XCTAssertEqual(value.filterRepos.map(\.shortName), ["x", "paused"])
    }

    func testOldPersistedDestinationFallsBackToOverview() {
        for old in ["Issue Queue", "Pipelines", "Tasks", "Workers", "", "unknown"] {
            XCTAssertEqual(Destination.restore(old), .overview, old)
        }
        XCTAssertEqual(Destination.restore("workers"), .workers)
        XCTAssertEqual(Destination.restore("pipelines"), .pipelines)
        XCTAssertEqual(Destination.allCases, [.overview, .pipelines, .workers, .projects])
        XCTAssertEqual(Destination.allCases.map(\.title), ["概览", "Pipelines", "Workers", "Projects"])
    }

    func testTaskTextIsChineseAndControllerTextStaysAsReceived() throws {
        let task = #"{"id":"t","repo":"o/widgets","issue":7,"pr":0,"kind":"implement","state":"pending","stage":"pending","worker":"","attempt":0,"updated_at":"2026-11-04T00:00:00Z","tags":[],"subject":"Fix login"}"#
        let row = try snapshot(projects: "", tasks: task).board.tasks[0]
        XCTAssertEqual(row.displayTitle, "Fix login")
        XCTAssertEqual(row.repoShortName, "widgets")
        XCTAssertEqual(row.reference, "#7")
        XCTAssertEqual(row.kindZh, "实现")
        XCTAssertEqual(row.statusZh, "等待")
        XCTAssertEqual(row.reasonZh, "等待可用的 Worker 和名额。")
        let waiting = task.replacingOccurrences(of: #""tags":[]"#, with: #""tags":[],"waiting_reason":"no worker has tag gpu""#)
        XCTAssertEqual(try snapshot(projects: "", tasks: waiting).board.tasks[0].reasonZh, "no worker has tag gpu")
        // The menu-bar popover keeps its English wording.
        XCTAssertEqual(row.status, "Waiting")
    }

    func testRelativeAgeAndDurationAreChinese() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-30), now: now), "刚刚")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(30), now: now), "刚刚")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-300), now: now), "5 分钟前")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-3 * 3600 - 600), now: now), "3 小时前")
        XCTAssertEqual(relativeAge(now.addingTimeInterval(-3 * 86400), now: now), "3 天前")
        XCTAssertEqual(relativeAge(nil, now: now), "—")
        XCTAssertEqual(durationText(40), "40 秒")
        XCTAssertEqual(durationText(192), "3 分 12 秒")
        XCTAssertEqual(durationText(7500), "2 小时 5 分")
    }

    func testThroughputLabelFollowsSinceField() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(throughputLabel(Throughput(since: now.addingTimeInterval(-3600), done: 4, failed: 1), generatedAt: now), "近 1 小时完成 4、失败 1")
        XCTAssertEqual(throughputLabel(Throughput(since: now.addingTimeInterval(-86400), done: 2, failed: 0), generatedAt: now), "近 24 小时完成 2、失败 0")
        XCTAssertTrue(throughputLabel(Throughput(since: now.addingTimeInterval(-1234), done: 0, failed: 0), generatedAt: now).hasPrefix("自 "))
    }

    func testFindingTextIsRuleBasedAndThresholdShare() {
        func finding(_ rule: String?, _ age: Int?) -> Finding {
            Finding(taskId: "t", repo: nil, issue: nil, pr: nil, rule: rule, since: nil, ageSec: age, reason: "controller reason")
        }
        XCTAssertEqual(finding("recovery_stalled", 3 * 3600).text, "恢复挂起 3 小时")
        XCTAssertEqual(finding("finalizing_stalled", 25 * 60).text, "结果确认停滞 25 分钟")
        XCTAssertEqual(finding("result_stuck", 25 * 60).text, "结果写回失败 25 分钟")
        XCTAssertEqual(finding("result_stuck", 20 * 60).withinThresholdShare ?? 0, 0.5, accuracy: 0.0001)
        XCTAssertEqual(finding("dispatch_stalled", 15 * 60).text, "等待派发 15 分钟")
        XCTAssertEqual(finding("no_progress", 45 * 60).text, "45 分钟无进展")
        XCTAssertEqual(finding("new_rule", 60).text, "controller reason")
        XCTAssertEqual(finding("no_progress", nil).text, "controller reason")
        XCTAssertEqual(finding("recovery_stalled", 3600).withinThresholdShare ?? 0, 0.5, accuracy: 0.0001)
        XCTAssertEqual(finding("dispatch_stalled", 40 * 60).withinThresholdShare ?? 0, 0.25, accuracy: 0.0001)
        XCTAssertEqual(finding("no_progress", 10 * 60).withinThresholdShare, 1)
        XCTAssertNil(finding("no_progress", nil).withinThresholdShare)
    }

    func testFreshnessNeverCallsStaleConnected() {
        XCTAssertEqual(StatusSummary(title: "", symbol: "", count: "–", connected: false, kind: .stale).freshnessZh, "连接中断 · 显示最后已知数据")
        XCTAssertEqual(StatusSummary(title: "", symbol: "", count: "–", connected: false, kind: .unknown).freshnessZh, "未连接")
        XCTAssertEqual(StatusSummary(title: "", symbol: "", count: "–", connected: false, kind: .unavailable).freshnessZh, "Controller 不可用")
    }
}

final class QueueReasonArgumentTests: XCTestCase {
    func testAreaBusyKeepsThePRArgument() throws {
        let json = #"{"key":"o/r#1","position":1,"number":1,"title":"t","tier":"P2","state":"parked:area_busy:PR #622"}"#
        let row = try decoder().decode(IssueQueueRow.self, from: Data(json.utf8))
        XCTAssertEqual(row.reasonCode, "area_busy")
        XCTAssertEqual(row.detail, "和 PR #622 改动范围重叠")
    }

    func testAuthorAdmissionReasons() throws {
        let denied = #"{"key":"o/r#1","position":1,"number":1,"title":"t","tier":"P2","state":"blocked:author_not_admitted:guest"}"#
        let deniedRow = try decoder().decode(IssueQueueRow.self, from: Data(denied.utf8))
        XCTAssertEqual(deniedRow.reasonCode, "author_not_admitted")
        XCTAssertEqual(deniedRow.detail, "作者 guest 没有写权限，未启动")

        let deniedWithoutLogin = #"{"key":"o/r#2","position":2,"number":2,"title":"t","tier":"P2","state":"blocked:author_not_admitted"}"#
        let anonymousRow = try decoder().decode(IssueQueueRow.self, from: Data(deniedWithoutLogin.utf8))
        XCTAssertEqual(anonymousRow.detail, "作者没有写权限，未启动")

        let failed = #"{"key":"o/r#3","position":3,"number":3,"title":"t","tier":"P2","state":"blocked:author_check_failed"}"#
        let failedRow = try decoder().decode(IssueQueueRow.self, from: Data(failed.utf8))
        XCTAssertEqual(failedRow.detail, "作者权限查询失败，本轮未启动")
    }
}
