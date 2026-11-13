package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals
import com.google.protobuf.Timestamp
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectSummary
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// These projections consume values only; they do not need a relay populated
// with 122 encrypted sessions before each assertion.
class SessionListingTest {
    // --- projects from the machines' summaries and the sessions held ---

    private fun summary(path: String, ended: Int, created: Long = 0, endedAt: Long = 0) =
        ProjectSummary.newBuilder()
            .setPath(path)
            .setEnded(ended)
            .setLatestCreatedMs(created)
            .setLatestEndedMs(endedAt)
            .build()

    private fun machine(id: String, name: String, vararg projects: ProjectSummary) =
        MachineView(
            id,
            online = true,
            lastSeenMs = 0,
            meta =
                MachineMeta.newBuilder().setHostname(name).addAllProjects(projects.toList()).build(),
        )

    private fun view(
        id: String,
        machine: String,
        cwd: String,
        created: Long,
        exited: Boolean = false,
    ) =
        SessionView(
            id,
            machine,
            Session.newBuilder()
                .setCwd(cwd)
                .setCreatedAt(Timestamp.newBuilder().setSeconds(created / 1000))
                .setStatus(
                    if (exited) SessionStatus.SESSION_STATUS_EXITED
                    else SessionStatus.SESSION_STATUS_IDLE
                )
                .build(),
            updatedMs = created,
        )

    @Test
    fun aProjectAddedButNotStartedInYetIsListedFromItsSettings() {
        fun settings(path: String, machine: String, at: Long) =
            io.github.lewin671.remoteagent.proto.ProjectSettings.newBuilder()
                .setPath(path)
                .setMachineId(machine)
                .setUpdatedMs(at)
                .build()
        val st =
            RemoteState(
                machines =
                    mapOf(
                        "m1" to
                            machine(
                                "m1",
                                "mac",
                                summary("/code/old", 9, created = 1_000_000, endedAt = 2_000_000),
                            )
                    ),
                sessions =
                    listOf(view("a", "m1", "/code/foo", 4_000_000, exited = true)).associateBy {
                        it.id
                    },
                prefs =
                    PrefsObject(
                        1,
                        io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder()
                            .setSchema(1)
                            .addAllSettings(
                                listOf(
                                    settings("/code/added", "m1", 5_000_000),
                                    // Those with sessions are as their sessions and summaries say.
                                    settings("/code/foo", "m1", 7_000_000),
                                    settings("/code/old", "m1", 7_000_000),
                                    // The machine chosen for it is no longer the account's.
                                    settings("/code/gone", "m9", 8_000_000),
                                )
                            )
                            .build(),
                    ),
            )
        assertEquals(listOf("/code/added", "/code/foo", "/code/old"), projects(st, copy = ::messageTextForTest).map { it.key })
        val added = projects(st, copy = ::messageTextForTest).first()
        assertEquals(listOf("m1"), added.machineIds)
        assertEquals(5_000_000L, added.latestCreatedMs)
        assertEquals(0, added.ended)
        assertFalse(added.hasLive)
        assertTrue(added.sessions.isEmpty())
        assertEquals(4_000_000L, projects(st, copy = ::messageTextForTest)[1].latestCreatedMs)
        assertEquals(
            listOf("/code/added", "/code/foo", "/code/old"),
            listedProjects(st, copy = ::messageTextForTest).map { it.key },
        )
    }

    @Test
    fun projectsAreTheSummariesTogetherWithTheSessionsHeld() {
        val st =
            RemoteState(
                machines =
                    mapOf(
                        "m1" to
                            machine(
                                "m1",
                                "mac",
                                summary("/code/foo", 40, created = 5_000_000, endedAt = 6_000_000),
                                summary("/code/old", 9, created = 1_000_000, endedAt = 2_000_000),
                            ),
                        "m2" to
                            machine(
                                "m2",
                                "linux",
                                summary("/code/foo", 3, created = 7_000_000),
                                summary("/code/bar", 0, created = 3_000_000),
                            ),
                        // A machine whose metadata was not read yet has no summary.
                        "m3" to MachineView("m3", online = false, lastSeenMs = 0),
                    ),
                sessions =
                    listOf(
                            view("a", "m1", "/code/foo", 4_000_000),
                            view("b", "m1", "/code/foo", 2_000_000, exited = true),
                            view("c", "m2", "/code/bar", 9_000_000, exited = true),
                            view("d", "m3", "/code/new", 8_000_000),
                        )
                        .associateBy { it.id },
            )
        val ps = projects(st, copy = ::messageTextForTest).associateBy { it.key }
        // Live projects first, then by the latest creation, which is the later of the summaries'
        // and the sessions'.
        assertEquals(
            listOf("/code/new", "/code/foo", "/code/bar", "/code/old"),
            projects(st, copy = ::messageTextForTest).map { it.key },
        )
        val foo = ps.getValue("/code/foo")
        // Whether a session has not ended is the sessions' to say; how many ended is the
        // summaries', added up.
        assertTrue(foo.hasLive)
        assertEquals(43, foo.ended)
        assertEquals(7_000_000L, foo.latestCreatedMs)
        assertEquals(listOf("a", "b"), foo.sessions.map { it.id })
        // Its machines are those of both: the sessions' first, then the summaries'.
        assertEquals(listOf("m1", "m2"), foo.machineIds)
        assertEquals(listOf("mac", "linux"), foo.machines)
        // Only old sessions, none of them held: the project is still there.
        val old = ps.getValue("/code/old")
        assertFalse(old.hasLive)
        assertEquals(9, old.ended)
        assertEquals(emptyList<SessionView>(), old.sessions)
        assertEquals(listOf("m1"), old.machineIds)
        assertEquals(1_000_000L, old.latestCreatedMs)
        assertEquals(2_000_000L, old.updatedMs)
        assertEquals(listOf("/code/old"), old.paths)
        // A session held that a summary does not count yet is counted, and its creation wins when
        // later.
        val bar = ps.getValue("/code/bar")
        assertEquals(1, bar.ended)
        assertEquals(9_000_000L, bar.latestCreatedMs)
        // A project no summary names comes from its sessions alone.
        val new = ps.getValue("/code/new")
        assertEquals(0, new.ended)
        assertEquals(listOf("m3"), new.machineIds)

        // The lists, the default scope and the pickers all read this list.
        assertEquals(
            listOf("/code/new", "/code/foo", "/code/bar", "/code/old"),
            listedProjects(st, copy = ::messageTextForTest).map { it.key },
        )
        assertEquals(HomeScope.All, defaultScope(st, copy = ::messageTextForTest))
        assertEquals(old, project(st, "/code/old", copy = ::messageTextForTest))
        assertNull(project(st, "/code/none", copy = ::messageTextForTest))
        assertEquals(listOf("/code/foo", "/code/old"), machineProjects(st, "m1"))
        assertEquals(listOf("/code/foo", "/code/bar"), machineProjects(st, "m2"))
        assertEquals(listOf("/code/new"), machineProjects(st, "m3"))
        // Pinned and removed projects are arranged as before, whichever source they come from.
        val arranged =
            projectGroups(
                st.copy(
                    prefs =
                        PrefsObject(
                            1,
                            io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder()
                                .addPinned(
                                    io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder()
                                        .setPath("/code/old")
                                )
                                .addHidden(
                                    io.github.lewin671.remoteagent.proto.ProjectPref.newBuilder()
                                        .setPath("/code/bar")
                                )
                                .build(),
                        )
                )
            , copy = ::messageTextForTest)
        assertEquals(listOf("/code/old"), arranged.pinned.map { it.key })
        assertEquals(listOf("/code/new", "/code/foo"), arranged.others.map { it.key })
        assertEquals(listOf("/code/bar"), arranged.hidden.map { it.key })
        // The only project of an account whose sessions are all on later pages is the default
        // scope.
        val only =
            RemoteState(machines = mapOf("m1" to machine("m1", "mac", summary("/code/old", 9))))
        assertEquals(HomeScope.Of("/code/old"), defaultScope(only, copy = ::messageTextForTest))
    }

    @Test
    fun aPlaceInTheListingIsByEndTimeThenId() {
        val place = SessionPlace(100, "m")
        assertTrue(afterPlace(99, "a", place))
        assertTrue(afterPlace(100, "n", place))
        assertFalse(afterPlace(100, "m", place))
        assertFalse(afterPlace(100, "a", place))
        assertFalse(afterPlace(101, "z", place))
        // A session that has not ended is on the first page, never after a place.
        assertFalse(afterPlace(0, "z", place))
    }
}
