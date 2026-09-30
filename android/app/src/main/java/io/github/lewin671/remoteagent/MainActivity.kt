package io.github.lewin671.remoteagent

import android.Manifest
import android.content.Intent
import android.os.Build
import androidx.activity.result.contract.ActivityResultContracts
import io.github.lewin671.remoteagent.push.Notifier
import io.github.lewin671.remoteagent.push.Push
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
import androidx.compose.material.icons.filled.ChatBubbleOutline
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
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
import io.github.lewin671.remoteagent.ui.pal
import io.github.lewin671.remoteagent.ui.statusIn
import java.net.URLDecoder
import java.net.URLEncoder

class MainActivity : ComponentActivity() {
    /** A session to open, from a notification. */
    private val openSession = mutableStateOf<Pair<String, String?>?>(null)

    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {
        val app = application as RaApp
        app.remote.value?.let { r -> app.scope.launch { Push.register(app, r) } }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        openSession.value = target(intent)
        val app = application as RaApp
        setContent {
            val settings by app.prefs.settings.collectAsState(Settings())
            RaTheme(settings.theme) {
                Surface(Modifier.fillMaxSize(), color = pal.bg) {
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
        LaunchedEffect(remote) {
            if (Build.VERSION.SDK_INT >= 33 && !Push.permitted(this@MainActivity) && Push.available(this@MainActivity)) {
                askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        }
        CompositionLocalProvider(LocalRemote provides remote, LocalNav provides actions) {
            val entry by nav.currentBackStackEntryAsState()
            val route = entry?.destination?.route
            val tabs = route == "home" || route == "settings"
            Column(Modifier.fillMaxSize().safeDrawingPadding()) {
                NavHost(nav, "home", Modifier.weight(1f)) {
                    composable("home") { HomeScreen() }
                    composable("settings") { SettingsScreen() }
                    composable("session/{id}") { SessionScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("project/{key}?focus={focus}") {
                        ProjectScreen(dec(it.arguments?.getString("key")), it.arguments?.getString("focus") == "1")
                    }
                    composable("device/{id}") { DeviceScreen(it.arguments?.getString("id").orEmpty()) }
                    composable("pair") { PairScreen() }
                    composable("approval/{sid}/{aid}") { ApprovalPage(it.arguments?.getString("sid").orEmpty(), it.arguments?.getString("aid").orEmpty()) }
                    composable("plan/{sid}") { PlanPage(it.arguments?.getString("sid").orEmpty()) }
                    composable("newproject") { NewProjectScreen() }
                    composable("pick") { PickProjectScreen() }
                }
                if (tabs) BottomBar(remote, route, nav)
            }
        }
    }

    @Composable
    private fun BottomBar(remote: Remote, route: String?, nav: NavHostController) {
        val state by remote.state.collectAsState()
        val waiting = state.sessions.values.count { statusIn(state, it).tone == Tone.WAIT }
        NavigationBar(containerColor = pal.card) {
            NavigationBarItem(route == "home", { if (route != "home") nav.navigate("home") { popUpTo("home") { inclusive = true } } }, {
                BadgedBox({ if (waiting > 0) Badge { Text("$waiting") } }) { Icon(Icons.Default.ChatBubbleOutline, null) }
            }, label = { Text("会话") })
            NavigationBarItem(route == "settings", { if (route != "settings") nav.navigate("settings") { popUpTo("home") } }, {
                Icon(Icons.Default.Settings, null)
            }, label = { Text("设置") })
        }
    }

    private fun remember(nav: NavHostController) = Nav(
        back = { if (!nav.popBackStack()) finish() },
        session = { nav.navigate("session/$it") { launchSingleTop = true } },
        project = { nav.navigate("project/${enc(it)}") },
        projectNew = { nav.navigate("project/${enc(it)}?focus=1") },
        home = { nav.navigate("home") { popUpTo("home") { inclusive = true } } },
        settings = { nav.navigate("settings") },
        device = { nav.navigate("device/$it") },
        pair = { nav.navigate("pair") },
        approval = { sid, aid -> nav.navigate("approval/$sid/$aid") },
        plan = { nav.navigate("plan/$it") },
        newProject = { nav.navigate("newproject") },
        pickProject = { nav.navigate("pick") },
    )

    companion object {
        const val EXTRA_SESSION = "session"
        // Project keys are paths; keep them in one path segment.
        private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")
        private fun dec(s: String?) = URLDecoder.decode(s.orEmpty(), "UTF-8")
    }
}
