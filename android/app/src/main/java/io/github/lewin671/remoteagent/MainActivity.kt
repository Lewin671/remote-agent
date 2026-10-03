package io.github.lewin671.remoteagent

import android.content.Intent
import android.os.Build
import io.github.lewin671.remoteagent.push.Notifier
import kotlinx.coroutines.launch
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.TextButton
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.ui.T
import io.github.lewin671.remoteagent.ui.topLine
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.Tone
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.ui.ApprovalPage
import io.github.lewin671.remoteagent.ui.DeviceScreen
import io.github.lewin671.remoteagent.ui.HomeScreen
import io.github.lewin671.remoteagent.ui.JoinScreen
import io.github.lewin671.remoteagent.ui.LocalNav
import io.github.lewin671.remoteagent.ui.LocalRemote
import io.github.lewin671.remoteagent.ui.Nav
import io.github.lewin671.remoteagent.ui.NewProjectScreen
import io.github.lewin671.remoteagent.ui.PairScreen
import io.github.lewin671.remoteagent.ui.PickProjectScreen
import io.github.lewin671.remoteagent.ui.PlanPage
import io.github.lewin671.remoteagent.ui.ProjectScreen
import io.github.lewin671.remoteagent.ui.RaTheme
import io.github.lewin671.remoteagent.ui.SessionScreen
import io.github.lewin671.remoteagent.ui.SettingsScreen
import io.github.lewin671.remoteagent.ui.ManageProjectsScreen
import io.github.lewin671.remoteagent.ui.HiddenProjectsScreen
import io.github.lewin671.remoteagent.ui.OpenTerminal
import io.github.lewin671.remoteagent.ui.pal
import io.github.lewin671.remoteagent.ui.statusIn

class MainActivity : ComponentActivity() {
    /** A session to open, from a notification. */
    private val openSession = mutableStateOf<Pair<String, String?>?>(null)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        openSession.value = target(intent)
        val app = application as RaApp
        setContent {
            val settings by app.prefs.settings.collectAsState(Settings())
            RaTheme(settings.theme) {
                Surface(Modifier.fillMaxSize(), color = pal.card) {
                    val loaded by app.loaded.collectAsState()
                    val remote by app.remote.collectAsState()
                    val accountError by app.accountError.collectAsState()
                    val r = remote
                    when {
                        !loaded && accountError.isNotEmpty() -> Column(
                            Modifier.fillMaxSize().padding(24.dp),
                            verticalArrangement = Arrangement.Center,
                            horizontalAlignment = Alignment.CenterHorizontally,
                        ) {
                            Text(accountError)
                            TextButton(onClick = { app.loadAccount() }) { Text("重试") }
                        }
                        !loaded -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                        r == null -> JoinScreen(app)
                        else -> Main(r)
                    }
                }
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        target(intent)?.let { openSession.value = it }
    }

    private fun target(i: Intent?): Pair<String, String?>? =
        i?.getStringExtra(EXTRA_SESSION)?.let { it to i.getStringExtra(Notifier.EXTRA_APPROVAL) }

    @Composable
    private fun Main(remote: Remote) {
        val nav = rememberNavController()
        val actions = remember(nav)
        val target by openSession
        LaunchedEffect(target) {
            target?.let { (sid, approval) ->
                actions.session(sid)
                if (approval != null) actions.approval(sid, approval)
                openSession.value = null
            }
        }
        // Say what this client is, sealed, so other clients can tell it apart.
        val devices by remote.state.collectAsState()
        // The phone's own name where the system has one (小米 14), else its model number.
        val phone = remember { android.provider.Settings.Global.getString(contentResolver, android.provider.Settings.Global.DEVICE_NAME)?.trim().orEmpty().ifEmpty { Build.MODEL } }
        LaunchedEffect(devices.devices[remote.account.deviceId]?.metaRev, devices.conn) {
            val me = devices.devices[remote.account.deviceId]
            val description = "Android ${Build.VERSION.RELEASE} · $phone"
            if (me != null && (me.meta?.platform != "android" || me.meta?.description != description) && devices.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) {
                runCatching { remote.describeSelf("android", description) }
            }
        }
        CompositionLocalProvider(LocalRemote provides remote, LocalNav provides actions) {
            val entry by nav.currentBackStackEntryAsState()
            val route = entry?.destination?.route
            val tabs = route == "home" || route == "settings"
            // Asked once for this client's name, after joining and when it has none yet,
            // on the home and settings pages rather than over a session being read.
            val me = devices.devices[remote.account.deviceId]
            val asked = remember { getSharedPreferences("client-name", MODE_PRIVATE) }
            var naming by remember { mutableStateOf(!asked.contains(remote.account.deviceId)) }
            if (naming && tabs && me != null && me.meta?.name.isNullOrEmpty() && devices.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) {
                val scope = rememberCoroutineScope()
                val done = { asked.edit().putBoolean(remote.account.deviceId, true).apply(); naming = false }
                io.github.lewin671.remoteagent.ui.InputAlert(
                    "给这个客户端起个名字", phone.take(40), "如：我的手机", "保存", onDismiss = done, dismiss = "跳过",
                    text = "其他客户端的设备列表里靠它认出这一个，以后可以在「设置 › 设备」里改",
                ) { name -> if (name.isNotBlank()) scope.launch { runCatching { remote.renameDevice(me.id, name.trim()) } } }
            }
            Column(Modifier.fillMaxSize().safeDrawingPadding()) {
                NavHost(nav, "home", Modifier.weight(1f)) {
                    composable("home") { HomeScreen() }
                    composable("settings") { SettingsScreen() }
                    composable("session/{id}") { SessionScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("project/{key}?focus={focus}") {
                        ProjectScreen(routeValue(it.arguments?.getString("key")), it.arguments?.getString("focus") == "1")
                    }
                    composable("device/{id}") { DeviceScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("pair") { PairScreen() }
                    composable("approval/{sid}/{aid}") { ApprovalPage(it.arguments?.getString("sid").orEmpty(), it.arguments?.getString("aid").orEmpty()) }
                    composable("plan/{sid}") { PlanPage(it.arguments?.getString("sid").orEmpty()) }
                    composable("newproject") { NewProjectScreen() }
                    composable("newproject/{machine}/{path}") {
                        io.github.lewin671.remoteagent.ui.NewProjectAtScreen(it.arguments?.getString("machine").orEmpty(), routeValue(it.arguments?.getString("path")))
                    }
                    composable("pick") { PickProjectScreen() }
                    composable("projects") { ManageProjectsScreen() }
                    composable("quota/{id}") { io.github.lewin671.remoteagent.ui.QuotaScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("question/{sid}/{qid}") { io.github.lewin671.remoteagent.ui.QuestionPage(it.arguments?.getString("sid").orEmpty(), it.arguments?.getString("qid").orEmpty()) }
                    composable("image/{sid}/{seq}/{i}") {
                        io.github.lewin671.remoteagent.ui.ImageViewer(
                            it.arguments?.getString("sid").orEmpty(), it.arguments?.getString("seq")?.toLongOrNull() ?: 0, it.arguments?.getString("i")?.toIntOrNull() ?: 0,
                        )
                    }
                    composable("devices") { io.github.lewin671.remoteagent.ui.DevicesScreen() }
                    composable("client/{id}") { io.github.lewin671.remoteagent.ui.ClientScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("projects/hidden") { HiddenProjectsScreen() }
                }
                if (tabs) BottomBar(remote, route, nav)
            }
        }
    }

    @Composable
    private fun BottomBar(remote: Remote, route: String?, nav: NavHostController) {
        val state by remote.state.collectAsState()
        val waiting = state.sessions.values.count { statusIn(state, it).tone == Tone.WAIT }
        io.github.lewin671.remoteagent.ui.TabBar(route, waiting) { r ->
            if (r == "home") nav.navigate("home") { popUpTo("home") { inclusive = true } } else nav.navigate("settings") { popUpTo("home") }
        }
    }

    private fun remember(nav: NavHostController) = Nav(
        back = { if (!nav.popBackStack()) finish() },
        session = { nav.navigate("session/$it") { launchSingleTop = true } },
        project = { nav.navigate("project/${routeArg(it)}") },
        projectNew = { nav.navigate("project/${routeArg(it)}?focus=1") },
        home = { nav.navigate("home") { popUpTo("home") { inclusive = true } } },
        settings = { nav.navigate("settings") },
        device = { nav.navigate("device/$it") },
        pair = { nav.navigate("pair") },
        approval = { sid, aid -> nav.navigate("approval/$sid/$aid") },
        plan = { nav.navigate("plan/$it") },
        newProject = { nav.navigate("newproject") },
        pickProject = { nav.navigate("pick") },
        manageProjects = { nav.navigate("projects") },
        devices = { nav.navigate("devices") },
        quota = { nav.navigate("quota/$it") },
        question = { sid, qid -> nav.navigate("question/$sid/${routeArg(qid)}") },
        image = { sid, seq, i -> nav.navigate("image/$sid/$seq/$i") },
        client = { nav.navigate("client/$it") },
        hiddenProjects = { nav.navigate("projects/hidden") },
        newProjectAt = { machine, path -> nav.navigate("newproject/$machine/${routeArg(path)}") },
        sessionTerminal = { sid ->
            OpenTerminal.requested[sid] = true
            nav.navigate("session/$sid") { launchSingleTop = true }
        },
    )

    companion object {
        const val EXTRA_SESSION = "session"
    }
}

/**
 * A project key, which is an absolute path, as one route segment:
 * percent-encoded as a URI, so Navigation's own decoding gives the path back
 * (architecture §8). Everything but unreserved characters is escaped,
 * including "/", "%", "+", "?" and "&".
 */
fun routeArg(s: String): String = buildString {
    for (b in s.toByteArray(Charsets.UTF_8)) {
        val c = b.toInt() and 0xff
        if (c.toChar().let { it in 'A'..'Z' || it in 'a'..'z' || it in '0'..'9' || it in "-._~" }) append(c.toChar())
        else append('%').append("0123456789ABCDEF"[c shr 4]).append("0123456789ABCDEF"[c and 15])
    }
}

/** The key back from a route argument, which Navigation has already decoded once. */
fun routeValue(arg: String?): String = arg.orEmpty()
