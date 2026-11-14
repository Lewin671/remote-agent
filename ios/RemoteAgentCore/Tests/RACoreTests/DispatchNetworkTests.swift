import Foundation
import Testing
@testable import RACore

struct DispatchNetworkTests {
    @Test func aConsumedPostAndFailedMetadataCannotLosePriorEvidence() async throws {
        let listener=try Listener(),posts=Counter()
        listener.serve { fd in
            while let head=readHead(fd),let first=head.first {
                let length=head.first { $0.lowercased().hasPrefix("content-length:") }
                    .flatMap {Int($0.split(separator:":",maxSplits:1)[1].trimmingCharacters(in:.whitespaces))} ?? 0
                var received=0,buffer=[UInt8](repeating:0,count:max(1,length))
                while received<length {
                    let n=read(fd,&buffer,min(buffer.count,length-received));if n<=0 {return};received+=n
                }
                if first.hasPrefix("POST ") {posts.increment();return}
                if !writeAll(fd,Data("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\n\r\nok".utf8)){return}
            }
        }
        var listenerOpen = true
        defer { if listenerOpen { listener.close() } }
        let http=HTTP(),url=URL(string:"http://127.0.0.1:\(listener.port)")!

        _ = try await http.data(for:URLRequest(url:url))
        let state=DispatchState(prior:false),first=state.open(prior:false)
        let delivery=first.delivery();try delivery.dispatch()
        var post=URLRequest(url:url);post.httpMethod="POST";post.httpBody=Data("opaque-command".utf8)
        do {_ = try await http.data(for:post);Issue.record("lost response returned success")} catch {}
        #expect(posts.value==1)
        first.close()
        listener.close(); listenerOpen = false
        let retry=state.open(prior:false),preflight=retry.delivery()
        do {_ = try await http.data(for:URLRequest(url:url));Issue.record("metadata unexpectedly succeeded")} catch {
            // The failure occurred before this command's network handoff.
            preflight.notForwarded()
        }
        #expect(retry.hasPrior)
        #expect(retry.uncertain)
        retry.close()
    }
}
