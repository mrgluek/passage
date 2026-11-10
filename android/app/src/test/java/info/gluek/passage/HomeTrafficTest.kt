package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test

class HomeTrafficTest {
    private val idle = HomeSetup.TrafficCounters(2, 10, 20)

    @Test fun firstSnapshotAndUnchangedCountersAreSteady() {
        assertFalse(HomeSetup.trafficChanged(null, idle))
        assertFalse(HomeSetup.trafficChanged(idle, idle.copy()))
        assertEquals(0L, HomeSetup.trafficDeadline(null, idle, 100, 0))
    }

    @Test fun ConnectionsAndEitherByteCounterCountAsTraffic() {
        for (current in listOf(idle.copy(connections = 3), idle.copy(up = 11), idle.copy(down = 21))) {
            assertTrue(HomeSetup.trafficChanged(idle, current))
            assertEquals(4_100L, HomeSetup.trafficDeadline(idle, current, 100, 0))
        }
    }

    @Test fun quietPollKeepsDeadlineAndNewTrafficExtendsIt() {
        val changed = idle.copy(up = 11)
        assertEquals(4_100L, HomeSetup.trafficDeadline(changed, changed, 2_100, 4_100))
        assertEquals(7_000L, HomeSetup.trafficDeadline(changed, changed.copy(down = 22), 3_000, 4_100))
    }

    @Test fun offIsNeutralAndOnlyStartFailureIsRed() {
        assertEquals(HomeSetup.DotState.OFF, HomeSetup.dotState(false, false, true))
        assertEquals(HomeSetup.DotState.ON, HomeSetup.dotState(true, false, false))
        assertEquals(HomeSetup.DotState.TRAFFIC, HomeSetup.dotState(true, false, true))
        for (running in listOf(false, true)) assertEquals(HomeSetup.DotState.PROBLEM, HomeSetup.dotState(running, true, true))
    }
}
