package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test

class HomeSetupTest {
    @Test fun linkIsFirstEvenWhenRunningOrPreviouslySeen() {
        for (running in listOf(false, true)) for (seen in listOf(false, true)) {
            assertEquals(1, HomeSetup.step(false, running, seen))
        }
    }
    @Test fun setupFollowsServiceAndTraffic() {
        assertEquals(2, HomeSetup.step(true, false, false))
        assertEquals(2, HomeSetup.step(true, false, true))
        assertEquals(3, HomeSetup.step(true, true, false))
        assertEquals(4, HomeSetup.step(true, true, true))
    }
    @Test fun migrationUsesOnlyPreexistingRunningSettingOnce() {
        for (link in listOf(false, true)) for (running in listOf(false, true)) {
            assertEquals(link && running, HomeSetup.migrateAppSeen(null, link, running))
            assertFalse(HomeSetup.migrateAppSeen(false, link, running))
            assertTrue(HomeSetup.migrateAppSeen(true, link, running))
        }
    }
    @Test fun maskedPasswordNeverContainsTheValue() {
        for (password in listOf("", "secret", "a very long secret")) {
            assertEquals("••••••••", HomeSetup.password(password, false))
            assertEquals(password, HomeSetup.password(password, true))
        }
    }
    @Test fun appTabsStayOpenWhileWaitingAndRequireExpansionOnceConnected() {
        assertTrue(HomeSetup.appTabsVisible(connected = false, expanded = false))
        assertTrue(HomeSetup.appTabsVisible(connected = false, expanded = true))
        assertFalse(HomeSetup.appTabsVisible(connected = true, expanded = false))
        assertTrue(HomeSetup.appTabsVisible(connected = true, expanded = true))
        assertNull(HomeSetup.appToggleLabel(connected = false, expanded = false))
        assertNull(HomeSetup.appToggleLabel(connected = false, expanded = true))
        assertEquals(R.string.home_connect_another, HomeSetup.appToggleLabel(connected = true, expanded = false))
        assertEquals(R.string.home_hide, HomeSetup.appToggleLabel(connected = true, expanded = true))
    }
    @Test fun headingIsDoneOnlyWhenAllThreeStepsAreComplete() {
        assertEquals(R.string.home_steps_pending, HomeSetup.heading(hasLink = false, running = false, appSeen = false))
        assertEquals(R.string.home_steps_pending, HomeSetup.heading(hasLink = true, running = false, appSeen = false))
        assertEquals(R.string.home_steps_pending, HomeSetup.heading(hasLink = true, running = true, appSeen = false))
        assertEquals(R.string.home_steps_pending, HomeSetup.heading(hasLink = true, running = false, appSeen = true))
        assertEquals(R.string.home_steps_pending, HomeSetup.heading(hasLink = false, running = true, appSeen = true))
        assertEquals(R.string.home_steps_done, HomeSetup.heading(hasLink = true, running = true, appSeen = true))
    }

    @Test
    fun plainVerdictFollowsHealth() {
        assertEquals(HomeSetup.Plain.START_FAILED, HomeSetup.plain(true, true, true, "working", ""))
        assertEquals(HomeSetup.Plain.NOT_SET_UP, HomeSetup.plain(false, false, false, "", ""))
        assertEquals(HomeSetup.Plain.OFF, HomeSetup.plain(false, false, true, "", ""))
        assertEquals(HomeSetup.Plain.READY, HomeSetup.plain(true, false, true, "ready", "waiting_app"))
        assertEquals(HomeSetup.Plain.READY, HomeSetup.plain(true, false, false, "", ""))
        assertEquals(HomeSetup.Plain.CONNECTING, HomeSetup.plain(true, false, true, "starting", ""))
        assertEquals(HomeSetup.Plain.WORKING, HomeSetup.plain(true, false, true, "working", ""))
        assertEquals(HomeSetup.Plain.LIMITED, HomeSetup.plain(true, false, true, "limited", "reserve"))
        assertEquals(HomeSetup.Plain.NO_INTERNET, HomeSetup.plain(true, false, true, "problem", "no_internet"))
        assertEquals(HomeSetup.Plain.SERVER_DOWN, HomeSetup.plain(true, false, true, "problem", "server_down"))
        assertEquals(HomeSetup.Plain.NO_ROUTE, HomeSetup.plain(true, false, true, "problem", "no_route"))
        assertEquals(HomeSetup.Plain.SLOW, HomeSetup.plain(true, false, true, "problem", "slow"))
        assertEquals(HomeSetup.Plain.OFF, HomeSetup.plain(false, false, true, "working", ""))
    }

    @Test
    fun dotColourFollowsThePlainVerdict() {
        assertEquals(HomeSetup.DotState.PROBLEM, HomeSetup.dotFor(HomeSetup.Plain.NO_ROUTE, true))
        assertEquals(HomeSetup.DotState.PROBLEM, HomeSetup.dotFor(HomeSetup.Plain.START_FAILED, false))
        assertEquals(HomeSetup.DotState.WARN, HomeSetup.dotFor(HomeSetup.Plain.CONNECTING, true))
        assertEquals(HomeSetup.DotState.WARN, HomeSetup.dotFor(HomeSetup.Plain.SLOW, false))
        assertEquals(HomeSetup.DotState.OFF, HomeSetup.dotFor(HomeSetup.Plain.OFF, true))
        assertEquals(HomeSetup.DotState.TRAFFIC, HomeSetup.dotFor(HomeSetup.Plain.WORKING, true))
        assertEquals(HomeSetup.DotState.ON, HomeSetup.dotFor(HomeSetup.Plain.LIMITED, false))
    }
}
