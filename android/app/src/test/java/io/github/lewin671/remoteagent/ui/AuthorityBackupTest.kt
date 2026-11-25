package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import io.github.lewin671.remoteagent.core.proto.AccountSecurityViewModel
import io.github.lewin671.remoteagent.core.proto.HandoffRole
import io.github.lewin671.remoteagent.core.proto.HandoffViewModel
import io.github.lewin671.remoteagent.core.proto.SecurityBannerTone
import io.github.lewin671.remoteagent.core.proto.SecurityBannerView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AuthorityBackupTest {
    @get:Rule val ui = createComposeRule()
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})

    private fun show(content: @Composable () -> Unit) {
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalNav provides nav) { content() }
            }
        }
    }

    @Test
    fun handoffOfferRendersCodeAndErrorBannerFromViewModel() {
        var publishes = 0
        var vm by mutableStateOf(
            HandoffViewModel.newBuilder()
                .setRole(HandoffRole.HANDOFF_ROLE_INCOMING_CANDIDATE)
                .setCanPrepareCode(true)
                .build(),
        )
        show {
            HandoffPreparationContent(
                vm = vm,
                onPrepareCode = {
                    vm = vm.toBuilder().setPreparedCode("synthetic-offline-code").setCanPublishOffer(true).build()
                },
                onPublishOffer = {
                    publishes++
                    vm = if (publishes == 1) {
                        vm.toBuilder()
                            .setErrorBanner(
                                SecurityBannerView.newBuilder()
                                    .setVisible(true)
                                    .setTone(SecurityBannerTone.SECURITY_BANNER_TONE_ERROR)
                                    .setText(CoreText.newBuilder().setLiteral("save failed")),
                            )
                            .build()
                    } else {
                        vm.toBuilder()
                            .clearPreparedCode()
                            .clearErrorBanner()
                            .setHasPublishedOffer(true)
                            .build()
                    }
                },
            )
        }
        ui.onNodeWithText("我已保存，通知管理端").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        assertEquals(0, publishes)
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("我已保存，通知管理端").performScrollTo().performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("我已保存，通知管理端").performScrollTo().performClick()
        ui.onNodeWithText("恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。").assertExists()
        assertEquals(2, publishes)
    }

    @Test
    fun rotationRendersCodeAndErrorBannerFromViewModel() {
        var publishes = 0
        var vm by mutableStateOf(
            AccountSecurityViewModel.newBuilder()
                .setIsManager(true)
                .setCanPrepareRotation(true)
                .build(),
        )
        show {
            RotateRecoveryContent(
                vm = vm,
                onPrepare = {
                    vm = vm.toBuilder().setPreparedCode("synthetic-offline-code").build()
                },
                onPublish = {
                    publishes++
                    vm = vm.toBuilder()
                        .setErrorBanner(
                            SecurityBannerView.newBuilder()
                                .setVisible(true)
                                .setTone(SecurityBannerTone.SECURITY_BANNER_TONE_ERROR)
                                .setText(CoreText.newBuilder().setLiteral("save failed")),
                        )
                        .build()
                },
                onBack = {},
            )
        }
        ui.onNodeWithText("我已保存，发布换码").assertDoesNotExist()
        ui.onNodeWithText("生成新的恢复码").performClick()
        assertEquals(0, publishes)
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        ui.onNodeWithText("我已保存，发布换码").performScrollTo().performClick()
        ui.onNodeWithText("save failed").assertExists()
        ui.onNodeWithContentDescription("恢复码二维码").assertExists()
        assertEquals(1, publishes)
    }
}
