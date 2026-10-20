package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.DeviceMeta
import org.junit.Assert.assertSame
import org.junit.Test

class ClientIconTest {
    @Test fun clientIconsDescribeTheirPlatform() {
        assertSame(Lucide.Globe, clientIcon(DeviceMeta.newBuilder().setPlatform("web").build()))
        assertSame(Lucide.Smartphone, clientIcon(DeviceMeta.newBuilder().setPlatform("android").build()))
        assertSame(Lucide.Smartphone, clientIcon(DeviceMeta.newBuilder().setPlatform("ios").setDescription("iOS 27 · iPhone").build()))
        assertSame(Lucide.Tablet, clientIcon(DeviceMeta.newBuilder().setPlatform("ios").setDescription("iPadOS 27 · iPad").build()))
        assertSame(Lucide.Smartphone, clientIcon(null))
    }
}
