package io.github.lewin671.remoteagent

import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.foundation.background
import io.github.lewin671.remoteagent.ui.Motion
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.unit.IntOffset
import androidx.navigation.NavBackStackEntry
import androidx.navigation.NavGraphBuilder
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.push.Notifier
import io.github.lewin671.remoteagent.push.VisiblePushSession
import io.github.lewin671.remoteagent.ui.ApprovalPage
import io.github.lewin671.remoteagent.ui.CreateAccountScreen
import io.github.lewin671.remoteagent.ui.DeviceScreen
import io.github.lewin671.remoteagent.ui.HomeScreen
import io.github.lewin671.remoteagent.ui.JoinScreen
import io.github.lewin671.remoteagent.ui.LocalNav
import io.github.lewin671.remoteagent.ui.LocalRemote
import io.github.lewin671.remoteagent.ui.ManageProjectsScreen
import io.github.lewin671.remoteagent.ui.Nav
import io.github.lewin671.remoteagent.ui.NewProjectScreen
import io.github.lewin671.remoteagent.ui.OpenTerminal
import io.github.lewin671.remoteagent.ui.PairScreen
import io.github.lewin671.remoteagent.ui.PickProjectScreen
import io.github.lewin671.remoteagent.ui.PlanPage
import io.github.lewin671.remoteagent.ui.ProjectScreen
import io.github.lewin671.remoteagent.ui.RaTheme
import io.github.lewin671.remoteagent.ui.RecoverScreen
import io.github.lewin671.remoteagent.ui.RecoveryScreen
import io.github.lewin671.remoteagent.ui.RemovedScreen
import io.github.lewin671.remoteagent.ui.SessionScreen
import io.github.lewin671.remoteagent.ui.SettingsScreen
import io.github.lewin671.remoteagent.ui.pal
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    /** A session to open, from a notification. */
    private val openSession = mutableStateOf<Pair<String, String?>?>(null)
    /** The device page, from a recovery notification. */
    private val openDevices = mutableStateOf(false)
    private val openScheduled = mutableStateOf(false)
    /** The computer whose 电脑状态 a disk notification opens. */
    private val openMachineStatus = mutableStateOf<String?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        io.github.lewin671.remoteagent.ui.Motion.observe(applicationContext)
        enableEdgeToEdge()
        openSession.value = target(intent)
        openMachineStatus.value = intent?.getStringExtra(Notifier.EXTRA_MACHINE_STATUS)
        openDevices.value = intent?.getBooleanExtra(Notifier.EXTRA_SECURITY, false) == true
        val app = application as RaApp
        setContent {
            val settings by app.prefs.settings.collectAsState(Settings())
            RaTheme(settings.theme) {
                val dark = pal.dark
                androidx.compose.runtime.SideEffect {
                    androidx.core.view.WindowCompat.getInsetsController(window, window.decorView).apply {
                        isAppearanceLightStatusBars = !dark
                        isAppearanceLightNavigationBars = !dark
                    }
                }
                Surface(Modifier.fillMaxSize(), color = pal.paper) {
                    val loaded by app.loaded.collectAsState()
                    val remote by app.remote.collectAsState()
                    val accountError by app.accountError.collectAsState()
                    val r = remote
                    when {
                        !loaded && accountError.isNotEmpty() -> io.github.lewin671.remoteagent.ui.AppSafeArea {
                            io.github.lewin671.remoteagent.ui.AccountLoadFailure(
                                accountError,
                                { app.loadAccount() },
                                { app.discardAccount() },
                            )
                        }
                        !loaded ->
                            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                                io.github.lewin671.remoteagent.ui.Spinner(pal.ink2, size = 24.dp)
                            }
                        r == null -> io.github.lewin671.remoteagent.ui.AppSafeArea { JoinScreen(app) }
                        r.state.collectAsState().value.removed -> io.github.lewin671.remoteagent.ui.AppSafeArea { RemovedScreen(app, r) }
                        // The first-level pages run under the system bars; every other page keeps clear of them.
                        else -> Main(r)
                    }
                }
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        target(intent)?.let { openSession.value = it }
        intent.getStringExtra(Notifier.EXTRA_MACHINE_STATUS)?.let { openMachineStatus.value = it }
        if (intent?.getBooleanExtra(Notifier.EXTRA_SECURITY, false) == true)
            openDevices.value = true
    }

    private fun target(i: Intent?): Pair<String, String?>? =
        // A disk notification names a computer, not a session.
        i?.takeIf { !it.hasExtra(Notifier.EXTRA_MACHINE_STATUS) }?.getStringExtra(EXTRA_SESSION)?.let {
            openScheduled.value = i.getBooleanExtra(Notifier.EXTRA_SCHEDULED, false)
            val seq = i.getLongExtra(Notifier.EXTRA_SCHEDULE_SEQ, 0)
            if (seq > 0) io.github.lewin671.remoteagent.ui.ScheduleJump.requested[it] = seq
            it to i.getStringExtra(Notifier.EXTRA_APPROVAL)
        }

    @Composable
    private fun Main(remote: Remote) {
        val nav = rememberNavController()
        val focus = LocalFocusManager.current
        val keyboard = androidx.compose.ui.platform.LocalSoftwareKeyboardController.current
        // Start dismissing input and navigating in the same callback; neither waits for the IME.
        val actions = remember(nav, remote) { io.github.lewin671.remoteagent.ui.dismissPageInput(focus, keyboard) }
        val notificationScope = rememberCoroutineScope()
        val target by openSession
        LaunchedEffect(openDevices.value) {
            if (openDevices.value) {
                actions.devices()
                openDevices.value = false
            }
        }
        val machineStatus by openMachineStatus
        LaunchedEffect(machineStatus) {
            machineStatus?.let {
                actions.machineStatus(it)
                openMachineStatus.value = null
            }
        }
        LaunchedEffect(target) {
            target?.let { (sid, approval) ->
                val scheduled = openScheduled.value
                if (scheduled) {
                    val destination = io.github.lewin671.remoteagent.ui.resolveScheduleNotificationRoute(
                        cached = { io.github.lewin671.remoteagent.ui.cachedScheduleNotificationRoute(sid, remote.schedules.tasks(), remote.state.value.sessions[sid]) },
                        load = {
                            remote.state.first { it.loaded || it.removed }
                            remote.fetchSession(sid)
                        },
                    )
                    when (destination) {
                        is io.github.lewin671.remoteagent.ui.ScheduleNotificationRoute.Task -> {
                            io.github.lewin671.remoteagent.ui.ScheduleJump.requested.remove(sid)
                            actions.task(destination.key.machine, destination.key.task)
                        }
                        io.github.lewin671.remoteagent.ui.ScheduleNotificationRoute.Conversation -> actions.session(sid)
                        io.github.lewin671.remoteagent.ui.ScheduleNotificationRoute.Inbox -> {
                            io.github.lewin671.remoteagent.ui.ScheduleJump.requested.remove(sid)
                            actions.schedules()
                        }
                    }
                    // Navigation is already complete; a slow/offline computer never blocks the tap.
                    notificationScope.launch {
                        kotlinx.coroutines.withTimeoutOrNull(3_000) {
                            val machine = (destination as? io.github.lewin671.remoteagent.ui.ScheduleNotificationRoute.Task)?.key?.machine
                                ?: remote.state.value.sessions[sid]?.machineId
                            if (machine != null && io.github.lewin671.remoteagent.core.scheduleWritable(remote.state.value, machine)) remote.schedules.refresh(machine)
                        }
                    }
                } else actions.session(sid)
                openScheduled.value = false
                if (approval != null) actions.approval(sid, approval)
                openSession.value = null
            }
        }
        // Say what this client is, sealed, so other clients can tell it apart.
        val devices by remote.state.collectAsState()
        // The phone's own name where the system has one (小米 14), else its model number.
        val phone = remember {
            android.provider.Settings.Global.getString(
                    contentResolver,
                    android.provider.Settings.Global.DEVICE_NAME,
                )
                ?.trim()
                .orEmpty()
                .ifEmpty { Build.MODEL }
        }
        val description = "Android ${Build.VERSION.RELEASE} · $phone"
        io.github.lewin671.remoteagent.ui.DeviceDescription(
            devices,
            remote.account.deviceId,
            description,
        ) {
            remote.describeSelf("android", description)
        }
        CompositionLocalProvider(LocalRemote provides remote, LocalNav provides actions) {
            val entry by nav.currentBackStackEntryAsState()
            val route = entry?.destination?.route
            LaunchedEffect(route) {
                if (route != null && route != "session/{id}" && route != "task/{machine}/{id}") remote.schedules.showList()
            }
            VisiblePushSession(
                RaApp.instance,
                entry?.takeIf { route == "session/{id}" }?.arguments?.getString("id"),
            )
            NavHost(
                nav,
                "home",
                Modifier.fillMaxSize(),
                enterTransition = { pageEnter(initialState, targetState) },
                exitTransition = { pageExit(initialState, targetState) },
                popEnterTransition = { pagePopEnter(initialState, targetState) },
                popExitTransition = { pagePopExit(initialState, targetState) },
            ) {
                composable("home") { io.github.lewin671.remoteagent.ui.HomeOrWide() }
                composable("schedules") { io.github.lewin671.remoteagent.ui.SchedulesScreen() }
                page("task/{machine}/{id}") { io.github.lewin671.remoteagent.ui.ScheduleDetailScreen(it.arguments?.getString("machine").orEmpty(), it.arguments?.getString("id").orEmpty()) }
                page("newtask?session={session}") { io.github.lewin671.remoteagent.ui.NewScheduleScreen(it.arguments?.getString("session").orEmpty()) }
                page("settings") { SettingsScreen() }
                // Its own edges: the terminal is dark up to the screen's, under the system bars.
                composable("session/{id}") { SessionScreen(it.arguments?.getString("id").orEmpty()) }
                page("project/{key}?focus={focus}") {
                    ProjectScreen(
                        routeValue(it.arguments?.getString("key")),
                        it.arguments?.getString("focus") == "1",
                    )
                }
                page("device/{id}?storage={storage}") { DeviceScreen(it.arguments?.getString("id").orEmpty(), scrollToStorage = it.arguments?.getString("storage") == "1") }
                page("machine/{id}") { io.github.lewin671.remoteagent.ui.MachineStatusScreen(it.arguments?.getString("id").orEmpty()) }
                page("pair?replace={replace}") {
                    PairScreen(it.arguments?.getString("replace").orEmpty())
                }
                page("recovery") { RecoveryScreen { nav.popBackStack() } }
                page("recover") { RecoverScreen(RaApp.instance) { nav.popBackStack() } }
                page("create") { CreateAccountScreen(RaApp.instance) { nav.popBackStack() } }
                page("approval/{sid}/{aid}") {
                    ApprovalPage(
                        it.arguments?.getString("sid").orEmpty(),
                        it.arguments?.getString("aid").orEmpty(),
                    )
                }
                page("plan/{sid}") { PlanPage(it.arguments?.getString("sid").orEmpty()) }
                page("newproject") { NewProjectScreen() }
                page("newproject/{machine}/{path}") {
                    io.github.lewin671.remoteagent.ui.NewProjectAtScreen(
                        it.arguments?.getString("machine").orEmpty(),
                        routeValue(it.arguments?.getString("path")),
                    )
                }
                page("pick") { PickProjectScreen() }
                page("projects") { ManageProjectsScreen() }
                page("quota/{id}") {
                    io.github.lewin671.remoteagent.ui.QuotaScreen(
                        it.arguments?.getString("id").orEmpty(),
                    )
                }
                page("question/{sid}/{qid}") {
                    io.github.lewin671.remoteagent.ui.QuestionPage(
                        it.arguments?.getString("sid").orEmpty(),
                        it.arguments?.getString("qid").orEmpty(),
                    )
                }
                page("image/{sid}/{seq}/{i}") {
                    io.github.lewin671.remoteagent.ui.ImageViewer(
                        it.arguments?.getString("sid").orEmpty(),
                        it.arguments?.getString("seq")?.toLongOrNull() ?: 0,
                        it.arguments?.getString("i")?.toIntOrNull() ?: 0,
                    )
                }
                page("licenses") { io.github.lewin671.remoteagent.ui.LicensesPage { nav.popBackStack() } }
                page("phonestorage") { io.github.lewin671.remoteagent.ui.PhoneImageStoragePage { nav.popBackStack() } }
                page("devices") { io.github.lewin671.remoteagent.ui.DevicesScreen() }
                page("client/{id}") {
                    io.github.lewin671.remoteagent.ui.ClientScreen(
                        it.arguments?.getString("id").orEmpty(),
                    )
                }
            }
        }
    }

    private fun remember(nav: NavHostController, remote: Remote, leave: () -> Unit): Nav {
        fun go(route: String, single: Boolean = false) {
            leave()
            nav.navigate(route) { launchSingleTop = single }
        }
        return Nav(
            back = {
                leave()
                if (!nav.popBackStack()) finish()
            },
            session = { go("session/$it", single = true) },
            project = { go("project/${routeArg(it)}") },
            projectNew = { go("project/${routeArg(it)}?focus=1") },
            // Home is the root: going there is going back, and it keeps its place.
            home = {
                leave()
                if (!nav.popBackStack("home", inclusive = false)) nav.navigate("home")
            },
            settings = { go("settings") },
            schedules = {
                leave()
                if (!nav.popBackStack("schedules", inclusive = false)) nav.navigate("schedules") { popUpTo("home"); launchSingleTop = true }
            },
            task = { machine, id ->
                val current = nav.currentBackStackEntry
                val sid = current?.arguments?.getString("id")
                if (current?.destination?.route == "session/{id}" && remote.state.value.sessions[sid]?.info?.taskOwned == true) {
                    leave()
                    nav.popBackStack()
                }
                val currentTask = nav.currentBackStackEntry
                if (currentTask?.destination?.route != "task/{machine}/{id}" || currentTask.arguments?.getString("id") != id || currentTask.arguments?.getString("machine") != machine) go("task/$machine/$id", true)
            },
            newTask = { go("newtask?session=$it") },
            startedSession = { sid ->
                leave()
                val page = nav.currentBackStackEntry
                val route = page?.destination?.route.orEmpty()
                nav.navigate("session/$sid") {
                    launchSingleTop = true
                    if (route.startsWith("project/") || route.startsWith("newproject/")) popUpTo(page!!.destination.id) { inclusive = true }
                }
            },
            scheduledSession = { sid, seq ->
                io.github.lewin671.remoteagent.ui.ScheduleJump.requested[sid] = seq
                go("session/$sid", single = true)
            },
            device = { go("device/$it") },
            machineStatus = { go("machine/$it", single = true) },
            deviceStorage = { go("device/$it?storage=1") },
            cleanup = { machine ->
                val state = remote.state.value
                val prompt = io.github.lewin671.remoteagent.ui.sessionText(UiMessages.machineStatusCleanupPrompt())
                val project = io.github.lewin671.remoteagent.ui.prepareCleanupDraft(state, machine, prompt)
                // A computer without a project yet: the folder chooser, and the next new session on it gets the words.
                if (project != null) go("project/${routeArg(project)}?focus=1") else go("newproject")
            },
            pair = { go("pair") },
            rejoin = { go("pair?replace=$it") },
            recovery = { go("recovery") },
            recover = { go("recover") },
            createAccount = { go("create") },
            approval = { sid, aid -> go("approval/$sid/$aid") },
            plan = { go("plan/$it") },
            newProject = { go("newproject") },
            pickProject = { go("pick") },
            manageProjects = { go("projects") },
            devices = { go("devices") },
            quota = { go("quota/$it") },
            question = { sid, qid -> go("question/$sid/${routeArg(qid)}") },
            image = { sid, seq, i -> go("image/$sid/$seq/$i") },
            client = { go("client/$it") },
            licenses = { go("licenses") },
            phoneStorage = { go("phonestorage") },
            newProjectAt = { machine, path -> go("newproject/$machine/${routeArg(path)}") },
            sessionTerminal = { sid ->
                OpenTerminal.requested[sid] = true
                go("session/$sid", single = true)
            },
        )
    }

    companion object {
        const val EXTRA_SESSION = "session"
    }
}

/**
 * A project key, which is an absolute path, as one route segment: percent-encoded as a URI, so
 * Navigation's own decoding gives the path back (architecture §8). Everything but unreserved
 * characters is escaped, including "/", "%", "+", "?" and "&".
 */
fun routeArg(s: String): String = buildString {
    for (b in s.toByteArray(Charsets.UTF_8)) {
        val c = b.toInt() and 0xff
        if (c.toChar().let { it in 'A'..'Z' || it in 'a'..'z' || it in '0'..'9' || it in "-._~" })
            append(c.toChar())
        else append('%').append("0123456789ABCDEF"[c shr 4]).append("0123456789ABCDEF"[c and 15])
    }
}

/** The key back from a route argument, which Navigation has already decoded once. */
fun routeValue(arg: String?): String = arg.orEmpty()

// Page transitions (design-system §4 动效, 慢). A page slides in from the right over the one it covers,
// which drifts a quarter of the way left; back plays it in reverse, a little quicker. Pages are
// opaque, so two never show through each other. Switching tabs swaps in place, and the image viewer
// fades over the page it opens from. With animations reduced the slides become short fades.
private val viewerFade get() = Motion.fadeInSpec(Motion.BASE)
private val pageFade get() = Motion.fadeInSpec(Motion.SLOW)
private fun <T> slideIn(): FiniteAnimationSpec<T> = Motion.enter(Motion.SLOW)
private fun <T> slideOut(): FiniteAnimationSpec<T> = Motion.exit(Motion.SLOW)
private val tabRoutes = setOf("home", "schedules")

private fun tabSwitch(from: NavBackStackEntry, to: NavBackStackEntry) =
    from.destination.route in tabRoutes && to.destination.route in tabRoutes

/**
 * Every page is drawn on the app's background, so one never shows through another as they slide,
 * and keeps clear of the system bars; the first-level pages run under them on their own.
 */
private fun NavGraphBuilder.page(route: String, content: @Composable (NavBackStackEntry) -> Unit) =
    composable(route) { Box(Modifier.fillMaxSize().background(pal.paper)) { io.github.lewin671.remoteagent.ui.AppSafeArea { content(it) } } }

private fun viewer(e: NavBackStackEntry) = e.destination.route?.startsWith("image/") == true

/** Whole-page editors need a transition independent of the changing IME height. */
private fun rises(e: NavBackStackEntry) = e.destination.route.let {
    it != null && (it.startsWith("project/") || it.startsWith("newproject/") || it.startsWith("newtask"))
}

// Fade through the app background: the two opaque pages never paint their text together.
// A vertical slide would change distance halfway through as the IME resizes the editor.
private fun editorEnter(): EnterTransition = if (Motion.reduced) EnterTransition.None else
    fadeIn(tween(Motion.FAST, delayMillis = Motion.BASE - Motion.FAST, easing = Motion.Decelerate))

private fun editorExit(): ExitTransition = if (Motion.reduced) ExitTransition.None else
    fadeOut(tween(Motion.BASE - Motion.FAST, easing = Motion.Accelerate))

private fun pageEnter(from: NavBackStackEntry, to: NavBackStackEntry): EnterTransition =
    when {
        tabSwitch(from, to) -> EnterTransition.None
        viewer(to) -> fadeIn(viewerFade)
        rises(to) -> editorEnter()
        Motion.reduced -> fadeIn(pageFade)
        else -> slideInHorizontally(slideIn()) { it }
    }

private fun pageExit(from: NavBackStackEntry, to: NavBackStackEntry): ExitTransition =
    when {
        tabSwitch(from, to) -> ExitTransition.None
        // Stays as it is under the viewer fading in.
        viewer(to) -> fadeOut(viewerFade, targetAlpha = 1f)
        rises(to) -> editorExit()
        Motion.reduced -> fadeOut(pageFade, targetAlpha = 1f)
        else -> slideOutHorizontally(slideIn()) { -it / 4 }
    }

private fun pagePopEnter(from: NavBackStackEntry, to: NavBackStackEntry): EnterTransition =
    when {
        tabSwitch(from, to) || viewer(from) -> EnterTransition.None
        rises(from) -> editorEnter()
        Motion.reduced -> fadeIn(pageFade)
        else -> slideInHorizontally(slideIn()) { -it / 4 }
    }

private fun pagePopExit(from: NavBackStackEntry, to: NavBackStackEntry): ExitTransition =
    when {
        tabSwitch(from, to) -> ExitTransition.None
        viewer(from) -> fadeOut(Motion.fadeOutSpec(Motion.BASE))
        rises(from) -> editorExit()
        Motion.reduced -> fadeOut(Motion.fadeOutSpec(Motion.SLOW))
        else -> slideOutHorizontally(slideOut()) { it }
    }