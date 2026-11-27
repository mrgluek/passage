import XCTest
@testable import Passage

final class PassageTests: XCTestCase {
    func testRouteSummaryAndOrdering() {
        let config: [String: JSONValue] = ["routes": .array([
            .object(["id": .string("second"), "type": .string("wss"), "priority": .number(95)]),
            .object(["id": .string("first"), "type": .string("wss"), "priority": .number(80)])
        ])]
        XCTAssertEqual(SavedRoute.list(config).map(\.summary), ["80 first (wss)", "95 second (wss)"])
    }

    func testRouteEdits() {
        let config: [String: JSONValue] = ["routes": .array([
            .object(["id": .string("one"), "type": .string("http"), "priority": .number(20)])
        ]), "route_edits": .object(["one": .object(["priority": .number(90), "off": .bool(true)])])]
        XCTAssertEqual(SavedRoute.list(config).first?.summary, "90 one (http)")
        XCTAssertEqual(SavedRoute.list(config).first?.off, true)
    }

    func testStoppedAndRunningStatusDecoding() throws {
        let decoder = JSONDecoder()
        XCTAssertFalse(try decoder.decode(CoreStatus.self, from: Data(#"{"running":false}"#.utf8)).running)
        let status = try decoder.decode(CoreStatus.self, from: Data(#"{"running":true,"route":"tunnel","via":"wss","mode":"auto","state":"direct","tunnels":null,"groups":null}"#.utf8))
        XCTAssertEqual(status.route, "tunnel")
        XCTAssertEqual(status.via, "wss")
        XCTAssertNil(status.groups)
    }

    func testErrorPayloadDecoding() throws {
        let error = try JSONDecoder().decode(CoreError.self, from: Data(#"{"error":"invalid import request"}"#.utf8))
        XCTAssertEqual(error.localizedDescription, "invalid import request")
    }

    func testImportRequestEncodingPreservesConfig() throws {
        let config: [String: JSONValue] = ["routes": .array([.object([
            "id": .string("one"), "params": .object(["host": .string("example.org")])
        ])])]
        for replace in [false, true] {
            let request = ImportRequest(existing: config, link: "example.org/#sample", replace: replace)
            let data = try JSONEncoder().encode(request)
            let encoded = try JSONDecoder().decode([String: JSONValue].self, from: data)
            XCTAssertEqual(encoded["existing"], .object(config))
            XCTAssertEqual(encoded["link"], .string(request.link))
            XCTAssertEqual(encoded["replace"], .bool(replace))
        }
    }
}
