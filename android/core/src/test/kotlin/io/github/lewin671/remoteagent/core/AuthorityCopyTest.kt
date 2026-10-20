package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import org.junit.Assert.*
import org.junit.Test

class AuthorityCopyTest {
    @Test
    fun leaseErrorsDirectTheUserToTheActualAuthorityRegardlessOfPlatform() {
        val failure = DeviceSecurity.Failure("lease")
        for (platform in listOf("android", "web", "ios", "")) {
            val authority = DeviceView("authority", DeviceKind.DEVICE_KIND_CLIENT, 0,
                DeviceMeta.newBuilder().setName("Work Client").setPlatform(platform).build())
            val member = RemoteState(authorityDeviceId = authority.id, devices = mapOf(authority.id to authority))
            assertTrue(errorText(failure, member).contains("请打开Work Client续期"))
            assertTrue(errorText(failure, member.copy(isAuthority = true)).contains("现在续期"))
        }
        assertTrue(errorText(failure).contains("请在当前管理端续期"))
    }
}
