package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test

class BootReceiverTest {
    @Test fun startsOnlyWhenSettingAndProxyAreOn() {
        assertTrue(BootReceiver.shouldStart(startOnBoot = true, running = true))
        assertFalse(BootReceiver.shouldStart(startOnBoot = true, running = false))
        assertFalse(BootReceiver.shouldStart(startOnBoot = false, running = true))
        assertFalse(BootReceiver.shouldStart(startOnBoot = false, running = false))
    }
}
