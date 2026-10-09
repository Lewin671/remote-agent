import Foundation
import Testing

@testable import RACore

/// The cases of "paths in tool titles" in web/src/model.test.ts, one for one
/// (copied from there by way of Android's ShortPathsTest): the clients must show the same.
@Suite struct ShortPathsTests {
    // title, cwd, home, shown
    private let cases: [[String]] = [
        ["/Users/me/Code/foo/auth/login.go", "/Users/me/Code/foo", "/Users/me", "auth/login.go"],
        ["/private/tmp/ra/proj/a.txt", "/tmp/ra/proj", "/Users/me", "a.txt"],
        ["/tmp/ra/proj/a.txt", "/private/tmp/ra/proj", "/Users/me", "a.txt"],
        ["cd /Users/me/Code/foo && ls", "/Users/me/Code/foo", "/Users/me", "cd . && ls"],
        ["cat /Users/me/.zshrc", "/Users/me/Code/foo", "/Users/me", "cat ~/.zshrc"],
        ["ls /Users/me/Code/foo-bar/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo-bar/x"],
        ["go build -o=/Users/me/Code/foo/bin/app ./cmd", "/Users/me/Code/foo/", "/Users/me", "go build -o=bin/app ./cmd"],
        ["diff /Users/me/Code/foo/a.go /Users/me/Code/bar/a.go", "/Users/me/Code/foo", "/Users/me", "diff a.go ~/Code/bar/a.go"],
        ["sed -n 1,5p \"/Users/me/Code/foo/main.go\"", "/Users/me/Code/foo", "/Users/me", "sed -n 1,5p \"main.go\""],
        ["Read /Users/me/Code/foo/auth/login.go:58", "/Users/me/Code/foo", "/Users/me", "Read auth/login.go:58"],
        ["cat /etc/hosts /private/etc/hosts", "/Users/me/Code/foo", "/Users/me", "cat /etc/hosts /private/etc/hosts"],
        ["curl https://example.com/Users/me/x", "/Users/me/Code/foo", "/Users/me", "curl https://example.com/Users/me/x"],
        ["grep -rn 'a/b' src/Users/me", "/Users/me/Code/foo", "/Users/me", "grep -rn 'a/b' src/Users/me"],
        ["tail /home/me/app/log.txt", "/srv/app", "/home/me", "tail ~/app/log.txt"],
        ["tail /home/me/app/log.txt", "/srv/app", "", "tail ~/app/log.txt"],
        ["cat /Users/me/notes.md", "", "/Users/me", "cat ~/notes.md"],
        ["/Users/me", "/Users/me/Code/foo", "/Users/me", "~"],
        ["Read /Users/me/Code/foo[archive]/a", "/Users/me/Code/foo", "/Users/me", "Read ~/Code/foo[archive]/a"],
        ["cat \"/Users/me/Code/foo bar/a\"", "/Users/me/Code/foo", "/Users/me", "cat \"~/Code/foo bar/a\""],
        ["cat \"/Users/me/Code/foo/a b.go\"", "/Users/me/Code/foo", "/Users/me", "cat \"a b.go\""],
        ["ls /Users/me/Code/foo.bak/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo.bak/x"],
        ["(cd /Users/me/Code/foo/sub)", "/Users/me/Code/foo", "/Users/me", "(cd sub)"],
        ["cat /Users/me/Code/foo", "/Users/me/Code/foo", "/Users/me", "cat ."],
        ["echo /Users/meow/x", "/Users/me/Code/foo", "/Users/me", "echo /Users/meow/x"],
        ["Read /Users/me/Code/foo[x]", "/Users/me/Code/foo", "", "Read ~/Code/foo[x]"],
        ["tail /home/me/app:12", "/srv/app", "/home/me", "tail ~/app:12"],
    ]

    @Test func matchesTheWebClient() {
        #expect(cases.count == 26)
        for c in cases { #expect(shortenPaths(c[0], cwd: c[1], home: c[2]) == c[3], "\(c[0])") }
    }

    // The cases of "whole paths (file edit titles)" in web/src/model.test.ts.
    private let wholePaths: [[String]] = [
        ["/Users/me/Code/foo bar/a.go", "/Users/me/Code/foo", "/Users/me", "~/Code/foo bar/a.go"],
        ["/Users/me/Code/foo/sub dir/b.go", "/Users/me/Code/foo", "/Users/me", "sub dir/b.go"],
        ["/private/tmp/p/x y.txt", "/tmp/p", "/Users/me", "x y.txt"],
        ["/etc/x y", "/Users/me/Code/foo", "/Users/me", "/etc/x y"],
    ]

    @Test func takesAFileEditTitleAsOnePath() {
        for c in wholePaths { #expect(shortenPath(c[0], cwd: c[1], home: c[2]) == c[3], "\(c[0])") }
    }

    // --- not in Android's tests ---

    @Test func aFolderIsMatchedByCodeUnitsNotByCharacters() {
        // A combining accent after the folder's last letter makes one Swift
        // Character of them, so hasPrefix says no where the JVM says yes; what
        // follows is then not /, so the path stays whole either way round.
        #expect(shortenPath("/Users/me/Code/foo\u{301}/a", cwd: "/Users/me/Code/foo", home: "") == "~/Code/foo\u{301}/a")
        // Chinese folders and a line suffix.
        #expect(shortenPaths("打开 /Users/me/项目/笔记.md:12:3", cwd: "/Users/me/项目", home: "/Users/me") == "打开 笔记.md:12:3")
        #expect(shortenPaths("cat /Users/me/项目x/a", cwd: "/Users/me/项目", home: "/Users/me") == "cat ~/项目x/a")
        // A token ends at ASCII whitespace only: an ideographic space is part of the path.
        #expect(shortenPaths("cat /Users/me/a\u{3000}b c", cwd: "", home: "/Users/me") == "cat ~/a\u{3000}b c")
        #expect(shortPath("/Users/me/Code/foo") == "~/Code/foo")
        #expect(shortPath("/home/me") == "~")
        #expect(shortPath("/srv/Users/me") == "/srv/Users/me")
    }
}
