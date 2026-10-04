package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PrefsObject
import io.github.lewin671.remoteagent.core.PrefsOp
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.applySettings
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentInfo
import io.github.lewin671.remoteagent.proto.AgentSettings
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSettings
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.ApprovalMode
import org.junit.Assert.assertEquals
import org.junit.Test

class ProjectTest {
    @Test fun defaultModelResolvesSupportedEffortsWithoutPinningTheModel() {
        val models = io.github.lewin671.remoteagent.proto.ListModelsResponse.newBuilder().setDefaultModel("fallback")
            .addModels(io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("fallback").addEfforts("low"))
            .addModels(io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("configured").addEfforts("high").addEfforts("ultra"))
            .build()
        val settings = io.github.lewin671.remoteagent.proto.DaemonSettings.newBuilder().setDefaultModel("configured").build()
        val c = NewConfig("mac", "/work/app", effort = "high")
        assertEquals(listOf("high", "ultra"), projectModel(c, models, settings)?.effortsList)
        assertEquals(listOf("low"), projectModel(c, models)?.effortsList)
        assertEquals("", c.agentSettings().model)
        assertEquals("high", c.agentSettings().effort)
        org.junit.Assert.assertNull(projectModel(c.copy(model = "unknown"), models, settings))
    }

    private val path = "/work/app"

    private fun set(agent: Agent, model: String = "", effort: String = "", mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_UNSPECIFIED, standard: Boolean = false) =
        AgentSettings.newBuilder().setAgent(agent).setModel(model).setEffort(effort).setMode(mode).setStandardContext(standard).build()

    private fun saved(machine: String, agent: Agent = Agent.AGENT_UNSPECIFIED, vararg sets: AgentSettings) =
        ProjectSettings.newBuilder().setPath(path).setMachineId(machine).setAgent(agent).addAllAgents(sets.toList()).build()

    private fun session(id: String, machine: String, agent: Agent, model: String, at: Long, mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_UNSPECIFIED, effort: String = "") =
        SessionView(id, machine, Session.newBuilder().setCwd(path).setModel(model).setEffort(effort).setAgent(agent).setMode(mode).build(), updatedMs = at)

    private fun host(id: String, vararg agents: Agent, online: Boolean = true) =
        MachineView(id, online, 0, MachineMeta.newBuilder().addAllAgents(agents.map { AgentInfo.newBuilder().setAgent(it).build() }).build())

    private val both = arrayOf(Agent.AGENT_CODEX, Agent.AGENT_CLAUDE)

    private fun state(saved: ProjectSettings? = null, sessions: List<SessionView> = emptyList(), vararg machines: MachineView) = RemoteState(
        prefs = PrefsObject(1, ProjectPrefs.newBuilder().apply { if (saved != null) addSettings(saved) }.build()),
        sessions = sessions.associateBy { it.id }, machines = machines.associateBy { it.id })

    @Test
    fun savedSettingsWinOverLatestSessionAndStayOnOfflineMachine() {
        val stored = saved("old", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE, "sonnet", "high", ApprovalMode.APPROVAL_MODE_FULL_ACCESS, standard = true))
        val st = state(stored, listOf(session("s", "new", Agent.AGENT_CODEX, "astra", 999)), host("old", *both, online = false), host("new", *both))
        val config = projectConfig(st, path)!!
        assertEquals(NewConfig("old", path, Agent.AGENT_CLAUDE, "sonnet", "high", Mode.FULL, standardContext = true), config)
        assertEquals(stored.agentsList.single(), config.agentSettings())
    }

    @Test
    fun aProjectWhoseSessionsAreAllOnPagesNotFetchedStartsOnTheMachineItsSummaryNames() {
        // The listing comes a page at a time: no session of the project is held, only the machines' summaries name it.
        fun summed(id: String, created: Long?) = MachineView(id, true, 0, MachineMeta.newBuilder().addAgents(AgentInfo.newBuilder().setAgent(Agent.AGENT_CLAUDE))
            .apply { if (created != null) addProjects(io.github.lewin671.remoteagent.proto.ProjectSummary.newBuilder().setPath(path).setEnded(4).setLatestCreatedMs(created)) }.build())
        val st = state(null, emptyList(), summed("idle", null), summed("earlier", 100), summed("latest", 200))
        // The machine with the project's newest session, not just any machine.
        assertEquals(NewConfig("latest", path, Agent.AGENT_CLAUDE), projectConfig(st, path))
        // The machines offered for it are those the summaries name, and every machine for a folder nothing names.
        assertEquals(listOf("latest", "earlier"), projectMachines(st, path))
        assertEquals(listOf("idle", "earlier", "latest"), projectMachines(st, "/work/other"))
        assertEquals(null, projectConfig(st, "/work/other"))
        // A session held here still says more than a summary: the latest one's machine comes first.
        val held = state(null, listOf(session("s", "earlier", Agent.AGENT_CLAUDE, "sonnet", 999)), summed("earlier", 100), summed("latest", 200))
        assertEquals("earlier", projectConfig(held, path)!!.machineId)
        assertEquals(listOf("earlier", "latest"), projectMachines(held, path))
    }

    @Test
    fun theCliIsThePickThenTheRememberedOneThenTheLatestSessionsThenTheMachinesFirst() {
        val mac = host("mac", *both)
        val sessions = listOf(session("a", "mac", Agent.AGENT_CLAUDE, "sonnet", 20), session("b", "mac", Agent.AGENT_CODEX, "astra", 10))
        // Nothing stored: the latest session's machine and CLI.
        assertEquals(Agent.AGENT_CLAUDE, projectConfig(state(null, sessions, mac), path)!!.agent)
        // The remembered CLI wins over the latest session's.
        val remembered = state(saved("mac", Agent.AGENT_CODEX), sessions, mac)
        assertEquals(Agent.AGENT_CODEX, projectConfig(remembered, path)!!.agent)
        // The pick next to the message box wins over both.
        assertEquals(Agent.AGENT_CLAUDE, projectConfig(remembered, path, pick = Agent.AGENT_CLAUDE)!!.agent)
        // Settings that remember no CLI (stored before a session started) leave it to the latest session.
        assertEquals(Agent.AGENT_CLAUDE, projectConfig(state(saved("mac"), sessions, mac), path)!!.agent)
        // No session and nothing stored: the machine given, with its first CLI.
        org.junit.Assert.assertNull(projectConfig(state(null, emptyList(), mac), path))
        assertEquals(NewConfig("mac", path, Agent.AGENT_CLAUDE), projectConfig(state(null, emptyList(), host("mac", Agent.AGENT_CLAUDE, Agent.AGENT_CODEX)), path, machine = "mac"))
        // The stored machine wins over the latest session's.
        assertEquals("vps", projectConfig(state(saved("vps", Agent.AGENT_CODEX), sessions, mac, host("vps", *both)), path)!!.machineId)
    }

    @Test
    fun aCliTheMachineLacksFallsBackToItsFirstAndIsNamed() {
        val vps = host("vps", Agent.AGENT_CODEX)
        val stored = saved("vps", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE, "sonnet"), set(Agent.AGENT_CODEX, "astra", mode = ApprovalMode.APPROVAL_MODE_ASK))
        val st = state(stored, emptyList(), vps)
        val config = projectConfig(st, path)!!
        // The CLI the machine has, with its own set; what is remembered is not touched.
        assertEquals(NewConfig("vps", path, Agent.AGENT_CODEX, "astra", mode = Mode.ASK), config)
        assertEquals(Agent.AGENT_CLAUDE, missingAgent(st, config))
        assertEquals(Agent.AGENT_CLAUDE, wantedAgent(st, path))
        // With the CLI installed there is nothing to say.
        val ok = state(stored, emptyList(), host("vps", *both))
        org.junit.Assert.assertNull(missingAgent(ok, projectConfig(ok, path)!!))
        // A draft kept on the other CLI while the machine has both is not a fallback.
        org.junit.Assert.assertNull(missingAgent(ok, NewConfig("vps", path, Agent.AGENT_CODEX)))
        // Nothing remembered and no session: the machine's first CLI is not a fallback.
        val fresh = state(null, emptyList(), vps)
        org.junit.Assert.assertNull(missingAgent(fresh, NewConfig("vps", path, Agent.AGENT_CODEX)))
    }

    @Test
    fun aCliWithoutAStoredSetStartsFromItsOwnLatestSession() {
        val mac = host("mac", *both)
        val sessions = listOf(
            session("new-codex", "mac", Agent.AGENT_CODEX, "astra", 30, ApprovalMode.APPROVAL_MODE_PLAN, "low"),
            session("claude", "mac", Agent.AGENT_CLAUDE, "sonnet", 20, ApprovalMode.APPROVAL_MODE_AUTO, "high"),
            session("old-claude", "mac", Agent.AGENT_CLAUDE, "opus", 10, ApprovalMode.APPROVAL_MODE_ASK),
        )
        val stored = saved("mac", Agent.AGENT_CODEX, set(Agent.AGENT_CODEX, "sol"))
        val st = state(stored, sessions, mac)
        // Codex has an entry: it is used as stored, empty fields meaning the machine's defaults.
        assertEquals(NewConfig("mac", path, Agent.AGENT_CODEX, "sol"), projectConfig(st, path))
        // Claude Code has none: its own latest session, not the project's latest.
        assertEquals(NewConfig("mac", path, Agent.AGENT_CLAUDE, "sonnet", "high", Mode.AUTO), projectConfig(st, path, pick = Agent.AGENT_CLAUDE))
        // Plan is never carried over.
        assertEquals(NewConfig("mac", path, Agent.AGENT_CODEX, "astra", "low"), projectConfig(state(null, sessions, mac), path, pick = Agent.AGENT_CODEX))
        // An empty entry is the machine's defaults, not the latest session.
        val empty = state(saved("mac", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE)), sessions, mac)
        assertEquals(NewConfig("mac", path, Agent.AGENT_CLAUDE), projectConfig(empty, path))
        // No entry and no session on that CLI: the machine's defaults.
        assertEquals(set(Agent.AGENT_CLAUDE), agentSet(state(stored, sessions.take(1), mac), path, Agent.AGENT_CLAUDE))
    }

    // --- what a start inherits while the listing is only partly fetched (architecture §8 「会话列表分页」) ---

    /** An ended session as a page of the listing brings it: [at] is when it ended, which is its place there. */
    private fun ended(id: String, agent: Agent, model: String, at: Long, machine: String = "mac", effort: String = "", mode: ApprovalMode = ApprovalMode.APPROVAL_MODE_UNSPECIFIED) = SessionView(
        id, machine,
        Session.newBuilder().setCwd(path).setModel(model).setEffort(effort).setAgent(agent).setMode(mode).setStatus(io.github.lewin671.remoteagent.proto.SessionStatus.SESSION_STATUS_EXITED)
            .setEndedAt(com.google.protobuf.Timestamp.newBuilder().setSeconds(at / 1000)).build(),
        updatedMs = at, listedEndedMs = at,
    )

    /** A machine whose summary says the project has [count] ended sessions. */
    private fun summed(id: String, count: Int, vararg agents: Agent) = MachineView(id, true, 0, MachineMeta.newBuilder()
        .addAllAgents(agents.map { AgentInfo.newBuilder().setAgent(it).build() })
        .addProjects(io.github.lewin671.remoteagent.proto.ProjectSummary.newBuilder().setPath(path).setEnded(count)).build())

    /** Pages fetched down to [cursor]; null for every page. */
    private fun paged(st: RemoteState, cursor: Long?) = st.copy(sessionsNext = cursor?.let { io.github.lewin671.remoteagent.core.SessionPlace(it, "") })

    @Test
    fun whatACliStartsWithIsNotSettledWhileItsLatestSessionMayBeOnAPageNotFetched() {
        val claude = ended("c1", Agent.AGENT_CLAUDE, "sonnet", 90_000, effort = "high", mode = ApprovalMode.APPROVAL_MODE_AUTO)
        val st = paged(state(null, listOf(claude), summed("mac", 9, *both)), 50_000)
        // The project's first session in the listing is held: the machine, the CLI and its set are settled.
        assertEquals(ProjectStart(NewConfig("mac", path, Agent.AGENT_CLAUDE, "sonnet", "high", Mode.AUTO), settled = true), projectStart(st, path))
        // Codex has no session among the pages fetched, and eight ended sessions are not fetched: one of them may be Codex's.
        val codex = projectStart(st, path, pick = Agent.AGENT_CODEX)!!
        assertEquals(false, codex.settled)
        assertEquals(NewConfig("mac", path, Agent.AGENT_CODEX), codex.config)
        assertEquals(false, agentSetSettled(st, path, Agent.AGENT_CODEX))
        assertEquals(true, agentSetSettled(st, path, Agent.AGENT_CLAUDE))
        // A stored set settles it, as does having every page, or every ended session of the project.
        assertEquals(true, projectStart(st.copy(prefs = PrefsObject(1, ProjectPrefs.newBuilder().addSettings(saved("mac", Agent.AGENT_CODEX, set(Agent.AGENT_CODEX, "sol"))).build())), path)!!.settled)
        assertEquals(true, projectStart(paged(st, null), path, pick = Agent.AGENT_CODEX)!!.settled)
        assertEquals(true, projectStart(paged(state(null, listOf(claude), summed("mac", 1, *both)), 50_000), path, pick = Agent.AGENT_CODEX)!!.settled)
        // The page with it came: settled, with its settings.
        val old = ended("x1", Agent.AGENT_CODEX, "astra", 40_000, effort = "low", mode = ApprovalMode.APPROVAL_MODE_ASK)
        val fetched = paged(state(null, listOf(claude, old), summed("mac", 9, *both)), 30_000)
        assertEquals(ProjectStart(NewConfig("mac", path, Agent.AGENT_CODEX, "astra", "low", Mode.ASK), settled = true), projectStart(fetched, path, pick = Agent.AGENT_CODEX))
    }

    @Test
    fun aSessionHeldPastThePagesFetchedDoesNotSettleIt() {
        // Fetched by its ID, or kept from before a reconnect: sessions not fetched yet come before it, and one of them may be the latest.
        val old = ended("x0", Agent.AGENT_CODEX, "old-model", 10_000)
        val st = paged(state(null, listOf(old), summed("mac", 9, *both)), 50_000)
        val start = projectStart(st, path)!!
        assertEquals(false, start.settled)
        // The guess is still the best one held.
        assertEquals("old-model", start.config.model)
        // The pages reached it: it is the first.
        assertEquals(ProjectStart(NewConfig("mac", path, Agent.AGENT_CODEX, "old-model"), settled = true), projectStart(paged(st, 9_000), path))
        // A session that has not ended is on the first page, always held: it settles it whatever else is not fetched.
        val live = session("l", "mac", Agent.AGENT_CODEX, "astra", 5)
        assertEquals(ProjectStart(NewConfig("mac", path, Agent.AGENT_CODEX, "astra"), settled = true), projectStart(paged(state(null, listOf(old, live), summed("mac", 9, *both)), 50_000), path))
    }

    @Test
    fun itIsSettledForTheCliTheSessionReallyStartsOn() {
        // Claude Code is remembered, with its set, and the machine only has Codex: the session starts on Codex,
        // whose latest session is on a page not fetched.
        val stored = saved("vps", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE, "sonnet"))
        val held = listOf(ended("c1", Agent.AGENT_CLAUDE, "sonnet", 90_000, machine = "vps"), ended("c2", Agent.AGENT_CLAUDE, "sonnet", 80_000, machine = "vps"))
        val st = paged(state(stored, held, summed("vps", 9, Agent.AGENT_CODEX)), 50_000)
        val start = projectStart(st, path)!!
        assertEquals(Agent.AGENT_CODEX, start.config.agent)
        assertEquals(false, start.settled)
        // With Claude Code installed its stored set is used: nothing to wait for.
        assertEquals(ProjectStart(NewConfig("vps", path, Agent.AGENT_CLAUDE, "sonnet"), settled = true), projectStart(paged(state(stored, held, summed("vps", 9, *both)), 50_000), path))
    }

    @Test
    fun beforeTheFirstPageCameNothingInheritedIsSettledUnlessTheCachedPageHasIt() {
        // At start only the cached page of an earlier run is held, and how many pages there are is not known.
        val claude = ended("c1", Agent.AGENT_CLAUDE, "sonnet", 90_000)
        val st = state(null, listOf(claude), summed("mac", 9, *both)).copy(sessionsListed = false)
        assertEquals(true, projectStart(st, path)!!.settled)
        assertEquals(false, projectStart(st, path, pick = Agent.AGENT_CODEX)!!.settled)
        // A project only the summaries name: its machine is a guess too.
        val none = state(null, emptyList(), summed("mac", 9, *both)).copy(sessionsListed = false)
        assertEquals(ProjectStart(NewConfig("mac", path, Agent.AGENT_CODEX), settled = false), projectStart(none, path))
        // A folder nothing names has nothing to inherit.
        assertEquals(ProjectStart(NewConfig("mac", "/work/new", Agent.AGENT_CODEX), settled = true),
            projectStart(none, "/work/new", machine = "mac")?.let { it.copy(config = it.config.copy(cwd = "/work/new")) })
    }

    @Test
    fun aDraftWrittenOverAGuessTakesTheSettingsOnceTheyAreKnown() {
        val guess = NewConfig("mac", path, Agent.AGENT_CODEX)
        val inherited = NewConfig("mac", path, Agent.AGENT_CODEX, "astra", "low", Mode.ASK)
        // Seeded with a guess, then written: the settings fetched replace the guess, and stay.
        val seeded = DraftConfig(guess, settled = false)
        seeded.follow(guess, drafting = true, settled = false)
        assertEquals(guess, seeded.value)
        seeded.follow(inherited, drafting = true, settled = true)
        assertEquals(inherited, seeded.value)
        seeded.follow(NewConfig("m2", path, Agent.AGENT_CLAUDE), drafting = true)
        assertEquals(inherited, seeded.value)
        // Written first, then switched to a CLI whose settings are not known yet: the same.
        val d = DraftConfig(NewConfig("mac", path, Agent.AGENT_CLAUDE, "sonnet"))
        d.follow(NewConfig("mac", path, Agent.AGENT_CLAUDE, "sonnet"), drafting = true)
        d.retarget(guess, settled = false)
        assertEquals(guess, d.value)
        d.follow(guess, drafting = true, settled = false)
        d.follow(inherited, drafting = true, settled = true)
        assertEquals(inherited, d.value)
        // A switch to a CLI whose settings are known is not a guess: the draft keeps it.
        d.retarget(inherited)
        d.follow(guess, drafting = true)
        assertEquals(inherited, d.value)
        // What the user chose on this page is never replaced, guess or not.
        val mine = NewConfig("mac", path, Agent.AGENT_CODEX, "sol", mode = Mode.FULL)
        val chosen = DraftConfig(guess, settled = false)
        chosen.choose(mine)
        chosen.follow(inherited, drafting = true, settled = true)
        assertEquals(mine, chosen.value)
        chosen.follow(inherited, drafting = false, settled = true)
        assertEquals(mine, chosen.value)
        // The draft of a project keeps whether its seed was a guess.
        val entry = Drafts.newSession("/work/guessed", guess.copy(cwd = "/work/guessed"), settled = false)
        entry.text.value = "先写着"
        entry.config.follow(inherited, drafting = true, settled = true)
        assertEquals(inherited, entry.config.value)
        assertEquals("先写着", entry.text.value)
    }

    @Test
    fun theFirstStartEstablishesTheSettingsAndLaterOnesOnlyFillIn() {
        val used = NewConfig("mac", path, Agent.AGENT_CLAUDE, "sonnet", "high", Mode.AUTO, standardContext = true)
        // Nothing stored: the machine, the CLI and its set.
        val first = startSettings(null, used, used)!!
        assertEquals(PrefsOp.SetSettings(path, "mac", agent = Agent.AGENT_CLAUDE, sets = listOf(used.agentSettings()), keepExisting = true), first)
        val stored = applySettings(null, first, 1)
        assertEquals(saved("mac", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE, "sonnet", "high", ApprovalMode.APPROVAL_MODE_AUTO, true)).toBuilder().setUpdatedMs(1).build(), stored)
        // The same CLI again changes nothing.
        org.junit.Assert.assertNull(startSettings(stored, used, used.copy(model = "opus")))
        // The other CLI: remembered, and its set added; Claude Code's stays.
        val codex = NewConfig("mac", path, Agent.AGENT_CODEX, "astra")
        val next = applySettings(stored, startSettings(stored, codex, codex)!!, 2)
        assertEquals(Agent.AGENT_CODEX, next.agent)
        assertEquals(listOf(codex.agentSettings(), used.agentSettings()), next.agentsList)
        // Back to Claude Code: only the remembered CLI changes.
        val back = applySettings(next, startSettings(next, used, used.copy(model = "opus"))!!, 3)
        assertEquals(Agent.AGENT_CLAUDE, back.agent)
        assertEquals(next.agentsList, back.agentsList)
    }

    @Test
    fun aMachinePickedJustForOneStartIsNotStored() {
        val project = NewConfig("mac", path, Agent.AGENT_CODEX, "astra", "high", Mode.ASK)
        val once = switchMachine(project, "vps", listOf(Agent.AGENT_CODEX))
        // Stored settings keep their machine, whatever the op carries.
        val stored = saved("mac", Agent.AGENT_CLAUDE, set(Agent.AGENT_CLAUDE, "sonnet"))
        val next = applySettings(stored, startSettings(stored, project, once)!!, 2)
        assertEquals("mac", next.machineId)
        // The set added is the project's, not the other machine's defaults.
        assertEquals(listOf(project.agentSettings(), set(Agent.AGENT_CLAUDE, "sonnet")), next.agentsList)
        // The first start: the project's machine is the one established.
        assertEquals("mac", applySettings(null, startSettings(null, project, once)!!, 1).machineId)
        // The other machine only had another CLI: remembered, with no set from a machine it does not belong to.
        val other = NewConfig("vps", path, Agent.AGENT_CLAUDE)
        assertEquals(PrefsOp.SetSettings(path, "mac", agent = Agent.AGENT_CLAUDE, keepExisting = true), startSettings(null, project, other))
    }

    @Test
    fun anotherMachineInProjectSettingsStartsEveryCliOver() {
        val stored = saved("mac", Agent.AGENT_CLAUDE, set(Agent.AGENT_CODEX, "astra"), set(Agent.AGENT_CLAUDE, "sonnet", mode = ApprovalMode.APPROVAL_MODE_AUTO))
        val moved = applySettings(stored, PrefsOp.SetSettings(path, "vps", moved = true), 5)
        val st = state(moved, listOf(session("s", "mac", Agent.AGENT_CLAUDE, "opus", 9)), host("vps", *both))
        // The empty entries stand for that machine's defaults: no model from the old machine or its sessions.
        assertEquals(NewConfig("vps", path, Agent.AGENT_CLAUDE), projectConfig(st, path))
        assertEquals(NewConfig("vps", path, Agent.AGENT_CODEX), projectConfig(st, path, pick = Agent.AGENT_CODEX))
    }

    @Test fun summaryUsesTheMachineModelDisplayName() {
        val models = io.github.lewin671.remoteagent.proto.ListModelsResponse.newBuilder()
            .addModels(io.github.lewin671.remoteagent.proto.ModelInfo.newBuilder().setSlug("gpt-6-astra").setDisplayName("GPT-6-Astra").setDefaultEffort("medium"))
            .build()
        val text = projectSummary(io.github.lewin671.remoteagent.core.RemoteState(),
            NewConfig("mac", "/work/app", model = "gpt-6-astra", mode = Mode.ASK), models)
        org.junit.Assert.assertTrue(text, text.endsWith("询问 · GPT-6-Astra"))
    }
    @Test fun summaryNamesTheMachineOnlyAmongSeveralAndCutsALongName() {
        fun mv(id: String, host: String) = io.github.lewin671.remoteagent.core.MachineView(id, true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder().setHostname(host).build())
        val one = io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("a" to mv("a", "MacBook")))
        assertEquals("", summaryMachine(one, "a"))
        val two = io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("a" to mv("a", "workstationM1-Pro-office-2024"), "b" to mv("b", "VPS")))
        assertEquals("workstationM1-P…", summaryMachine(two, "a"))
        assertEquals("VPS", summaryMachine(two, "b"))
    }

    @Test
    fun aNewSessionCannotBeStartedInPlan() {
        assertEquals(listOf(Mode.ASK, Mode.AUTO, Mode.FULL), newSessionModes)
    }

    private fun machine(vararg infos: io.github.lewin671.remoteagent.proto.AgentInfo) =
        io.github.lewin671.remoteagent.core.MachineView("m", true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder().addAllAgents(infos.toList()).build())

    private fun info(agent: Agent, mode: ApprovalMode, source: io.github.lewin671.remoteagent.proto.DefaultModeSource, varies: Boolean = false) =
        io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(agent).setDefaultMode(mode).setDefaultModeSource(source).setDefaultModeVaries(varies).build()

    private val cli = io.github.lewin671.remoteagent.proto.DefaultModeSource.DEFAULT_MODE_SOURCE_CLI_CONFIG

    @Test
    fun theCliDefaultTheMachineReportsIsShownWithoutAConfirmation() {
        // As measured on this machine: Claude Code defaults to bypass, Codex to auto.
        val m = machine(info(Agent.AGENT_CLAUDE, ApprovalMode.APPROVAL_MODE_FULL_ACCESS, cli, varies = true), info(Agent.AGENT_CODEX, ApprovalMode.APPROVAL_MODE_AUTO, cli))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(m, Agent.AGENT_CLAUDE)))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(m, Agent.AGENT_CODEX)))
        assertEquals("默认：完全放行（项目设置可能不同）", modeLabel(null, defaultModeOf(m, Agent.AGENT_CLAUDE), Agent.AGENT_CLAUDE))
        assertEquals("默认：自动", modeLabel(null, defaultModeOf(m, Agent.AGENT_CODEX), Agent.AGENT_CODEX))
        assertEquals("自动", effectiveModeText(null, defaultModeOf(m, Agent.AGENT_CODEX)))
        // A chosen mode needs no default.
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_ASK), launchMode(Mode.ASK, defaultModeOf(m, Agent.AGENT_CLAUDE)))
    }

    @Test
    fun anUnknownDefaultDoesNotBlockAStart() {
        // The daemon could not read it: it could be full access.
        val old = machine(io.github.lewin671.remoteagent.proto.AgentInfo.newBuilder().setAgent(Agent.AGENT_CODEX).build())
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(old, Agent.AGENT_CODEX)))
        // A mode outside the four (accept edits) is known and not full access.
        val edits = machine(info(Agent.AGENT_CLAUDE, ApprovalMode.APPROVAL_MODE_UNSPECIFIED, cli))
        assertEquals(LaunchMode.Send(ApprovalMode.APPROVAL_MODE_UNSPECIFIED), launchMode(null, defaultModeOf(edits, Agent.AGENT_CLAUDE)))
        assertEquals("默认：Claude Code 自己的模式", modeLabel(null, defaultModeOf(edits, Agent.AGENT_CLAUDE), Agent.AGENT_CLAUDE))
    }

    @Test
    fun onlyAStartedSessionTakesTheDraft() {
        // A refusal that asks for confirmation answers nothing: the draft stays.
        org.junit.Assert.assertNull(startedSession(null))
        org.junit.Assert.assertNull(startedSession(io.github.lewin671.remoteagent.proto.CommandResult.getDefaultInstance()))
        val ok = io.github.lewin671.remoteagent.proto.CommandResult.newBuilder().setCreateSession(
            io.github.lewin671.remoteagent.proto.CreateSessionResponse.newBuilder().setSession(io.github.lewin671.remoteagent.proto.Session.newBuilder().setId("s1")),
        ).build()
        assertEquals("s1", startedSession(ok))
    }

    @Test
    fun aDraftKeepsItsConfigurationWhenTheProjectChanges() {
        val first = NewConfig("m1", "/work/app", Agent.AGENT_CODEX, "gpt-a")
        val moved = NewConfig("m2", "/work/app", Agent.AGENT_CLAUDE)
        val d = DraftConfig(first)
        // Nothing written yet: it follows the project.
        d.follow(moved, drafting = false)
        assertEquals(moved, d.value)
        d.follow(first, drafting = false)
        // Once a draft is begun, an update does not retarget it.
        d.follow(moved, drafting = true)
        assertEquals(first, d.value)
        // Sent and cleared, it follows again.
        d.follow(moved, drafting = false)
        assertEquals(moved, d.value)
        // A choice stays even with nothing written.
        val mine = NewConfig("m1", "/work/app", Agent.AGENT_CODEX, mode = Mode.ASK)
        d.choose(mine)
        d.follow(first, drafting = false)
        assertEquals(mine, d.value)
    }

    @Test
    fun aReopenedDraftKeepsWhereItWasGoing() {
        val suggested = NewConfig("m1", "/work/reopen", Agent.AGENT_CODEX)
        val first = Drafts.newSession("/work/reopen", suggested)
        val mine = NewConfig("m2", "/work/reopen", Agent.AGENT_CLAUDE)
        first.config.choose(mine)
        first.text.value = "修一下登录"
        // Back out and in again, with the project now suggesting something else.
        val again = Drafts.newSession("/work/reopen", NewConfig("m3", "/work/reopen", Agent.AGENT_CODEX))
        assertEquals("修一下登录", again.text.value)
        assertEquals(mine, again.config.value)
    }
}

class SwitchTest {
    private val c = NewConfig("mac", "/p", io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, "gpt-6-astra", "medium", io.github.lewin671.remoteagent.core.Mode.AUTO)

    @org.junit.Test
    fun anotherCliTakesTheProjectsSetForIt() {
        val claude = NewConfig("mac", "/p", Agent.AGENT_CLAUDE, "sonnet", "high", Mode.ASK, standardContext = true)
        org.junit.Assert.assertEquals(claude, switchAgent(c, claude, Agent.AGENT_CLAUDE))
        // On a machine picked just for this start the model is that machine's default.
        val once = c.copy(machineId = "vps")
        org.junit.Assert.assertEquals(claude.copy(machineId = "vps", model = "", effort = ""), switchAgent(once, claude, Agent.AGENT_CLAUDE))
        // A project that knows nothing of that CLI: the machine's defaults.
        org.junit.Assert.assertEquals(NewConfig("mac", "/p", Agent.AGENT_CLAUDE), switchAgent(c, null, Agent.AGENT_CLAUDE))
    }

    @org.junit.Test
    fun anotherMachineKeepsTheCliOnlyWhenItHasIt() {
        val claude = c.copy(agent = io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE)
        val s = switchMachine(claude, "vps", listOf(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX))
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, s.agent)
        org.junit.Assert.assertEquals("", s.model)
        val kept = switchMachine(claude, "imac", listOf(io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX, io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE))
        org.junit.Assert.assertEquals(io.github.lewin671.remoteagent.proto.Agent.AGENT_CLAUDE, kept.agent)
    }

    @org.junit.Test
    fun theConfigurationIsTwoLines() {
        val state = io.github.lewin671.remoteagent.core.RemoteState()
        val (a, b) = configLines(state, c, DefaultMode(null, false, false))
        org.junit.Assert.assertTrue(a.endsWith("自动"))
        org.junit.Assert.assertEquals("gpt-6-astra · medium", b)
    }
}
