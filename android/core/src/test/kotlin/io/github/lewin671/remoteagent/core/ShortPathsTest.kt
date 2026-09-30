package io.github.lewin671.remoteagent.core

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The cases of "paths in tool titles" in web/src/model.test.ts, one for one
 * (copied from there): both clients must show the same.
 */
class ShortPathsTest {
    // title, cwd, home, shown
    private val cases = listOf(
        listOf("/Users/me/Code/foo/auth/login.go", "/Users/me/Code/foo", "/Users/me", "auth/login.go"),
        listOf("/private/tmp/ra/proj/a.txt", "/tmp/ra/proj", "/Users/me", "a.txt"),
        listOf("/tmp/ra/proj/a.txt", "/private/tmp/ra/proj", "/Users/me", "a.txt"),
        listOf("cd /Users/me/Code/foo && ls", "/Users/me/Code/foo", "/Users/me", "cd . && ls"),
        listOf("cat /Users/me/.zshrc", "/Users/me/Code/foo", "/Users/me", "cat ~/.zshrc"),
        listOf("ls /Users/me/Code/foo-bar/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo-bar/x"),
        listOf("go build -o=/Users/me/Code/foo/bin/app ./cmd", "/Users/me/Code/foo/", "/Users/me", "go build -o=bin/app ./cmd"),
        listOf("diff /Users/me/Code/foo/a.go /Users/me/Code/bar/a.go", "/Users/me/Code/foo", "/Users/me", "diff a.go ~/Code/bar/a.go"),
        listOf("sed -n 1,5p \"/Users/me/Code/foo/main.go\"", "/Users/me/Code/foo", "/Users/me", "sed -n 1,5p \"main.go\""),
        listOf("Read /Users/me/Code/foo/auth/login.go:58", "/Users/me/Code/foo", "/Users/me", "Read auth/login.go:58"),
        listOf("cat /etc/hosts /private/etc/hosts", "/Users/me/Code/foo", "/Users/me", "cat /etc/hosts /private/etc/hosts"),
        listOf("curl https://example.com/Users/me/x", "/Users/me/Code/foo", "/Users/me", "curl https://example.com/Users/me/x"),
        listOf("grep -rn 'a/b' src/Users/me", "/Users/me/Code/foo", "/Users/me", "grep -rn 'a/b' src/Users/me"),
        listOf("tail /home/me/app/log.txt", "/srv/app", "/home/me", "tail ~/app/log.txt"),
        listOf("tail /home/me/app/log.txt", "/srv/app", "", "tail ~/app/log.txt"),
        listOf("cat /Users/me/notes.md", "", "/Users/me", "cat ~/notes.md"),
        listOf("/Users/me", "/Users/me/Code/foo", "/Users/me", "~"),
        listOf("Read /Users/me/Code/foo[archive]/a", "/Users/me/Code/foo", "/Users/me", "Read ~/Code/foo[archive]/a"),
        listOf("cat \"/Users/me/Code/foo bar/a\"", "/Users/me/Code/foo", "/Users/me", "cat \"~/Code/foo bar/a\""),
        listOf("cat \"/Users/me/Code/foo/a b.go\"", "/Users/me/Code/foo", "/Users/me", "cat \"a b.go\""),
        listOf("ls /Users/me/Code/foo.bak/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo.bak/x"),
        listOf("(cd /Users/me/Code/foo/sub)", "/Users/me/Code/foo", "/Users/me", "(cd sub)"),
        listOf("cat /Users/me/Code/foo", "/Users/me/Code/foo", "/Users/me", "cat ."),
        listOf("echo /Users/meow/x", "/Users/me/Code/foo", "/Users/me", "echo /Users/meow/x"),
        listOf("Read /Users/me/Code/foo[x]", "/Users/me/Code/foo", "", "Read ~/Code/foo[x]"),
        listOf("tail /home/me/app:12", "/srv/app", "/home/me", "tail ~/app:12"),
    )

    @Test
    fun matchesTheWebClient() {
        assertEquals(26, cases.size)
        for ((title, cwd, home, shown) in cases) assertEquals(title, shown, shortenPaths(title, cwd, home))
    }
}
