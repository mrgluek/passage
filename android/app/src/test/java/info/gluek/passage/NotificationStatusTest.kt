package info.gluek.passage

import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

class NotificationStatusTest {
    @Test
    fun mapsHealthToHomeTitleAndReason() {
        val cases = listOf(
            Triple("starting", "", R.string.home_connecting_title to R.string.home_connecting_reason),
            Triple("ready", "waiting_app", R.string.home_ready_title to R.string.home_ready_reason),
            Triple("working", "", R.string.home_working_title to R.string.home_working_reason),
            Triple("limited", "reserve", R.string.home_working_title to R.string.home_limited_reason),
            Triple("problem", "no_internet", R.string.home_no_internet_title to R.string.home_no_internet_reason),
            Triple("problem", "server_down", R.string.home_server_down_title to R.string.home_server_down_reason),
            Triple("problem", "no_route", R.string.home_no_route_title to R.string.home_no_route_reason),
            Triple("problem", "slow", R.string.home_slow_title to R.string.home_slow_reason),
            Triple("problem", "unknown", R.string.home_slow_title to R.string.home_slow_reason),
        )
        for ((state, reason, expected) in cases) {
            val status = JSONObject().put("running", true)
                .put("health", JSONObject().put("state", state).put("reason", reason))
            assertEquals("$state/$reason", expected, StatusText.notificationResources(status, appSeen = false, fallbackRunning = true))
        }
    }

    @Test
    fun usesReadyCopyWhenHealthIsAbsent() {
        val status = JSONObject().put("running", true).put("route", "direct")
        assertEquals(
            R.string.home_ready_title to R.string.home_ready_reason,
            StatusText.notificationResources(status, appSeen = false, fallbackRunning = true),
        )
        assertEquals(
            R.string.home_ready_title to R.string.home_working_reason,
            StatusText.notificationResources(status, appSeen = true, fallbackRunning = true),
        )
        assertEquals(
            R.string.home_ready_title to R.string.home_ready_reason,
            StatusText.notificationResources(null, appSeen = false, fallbackRunning = true),
        )
    }

    @Test
    fun reportsOffWhenRouterIsStopped() {
        assertEquals(
            R.string.home_off_title to R.string.home_off_reason,
            StatusText.notificationResources(JSONObject().put("running", false), appSeen = false, fallbackRunning = true),
        )
        assertEquals(
            R.string.home_off_title to R.string.home_off_reason,
            StatusText.notificationResources(null, appSeen = false, fallbackRunning = false),
        )
    }
    @Test fun notificationAndPublicVersionShareTheStandaloneTitle() {
        val status = File("src/main/java/info/gluek/passage/StatusText.kt").readText()
        val service = File("src/main/java/info/gluek/passage/PassageService.kt").readText()
        assertTrue(status.contains("return c.getString(plain.title) to"))
        assertFalse(status.contains("R.string.status_title"))
        assertFalse(service.contains("R.string.status_title"))
        assertEquals(2, Regex("setContentTitle\\(title\\)").findAll(service).count())
        assertTrue(service.contains(".setPublicVersion("))
    }

    @Test fun networkLimitedTouchesOnlyTheBodyLine() {
        val status = JSONObject().put("running", true).put("network_limited", true)
            .put("health", JSONObject().put("state", "working"))
        assertEquals(
            R.string.home_working_title to R.string.home_working_reason,
            StatusText.notificationResources(status, appSeen = true, fallbackRunning = true),
        )
        val src = File("src/main/java/info/gluek/passage/StatusText.kt").readText()
        assertTrue(src.contains("return c.getString(plain.title) to HomeSetup.withNetworkLimited("))
        val service = File("src/main/java/info/gluek/passage/PassageService.kt").readText()
        assertFalse(service.contains("network_limited"))
        assertFalse(service.contains("R.string.network_limited"))
    }

    @Test fun fullStatusShowsCanaryTextOnlyWhenChecked() {
        val src = File("src/main/java/info/gluek/passage/StatusText.kt").readText()
        assertTrue(src.contains("!r.optBoolean(\"canary_checked\") -> \"\""))
    }

    @Test
    fun asksForALinkWhileThereAreNoRoutes() {
        val status = JSONObject().put("running", true).put("route_count", 0).put("subscriptions", org.json.JSONArray())
            .put("health", JSONObject().put("state", "starting"))
        assertEquals(
            HomeSetup.Plain.NOT_SET_UP.title to HomeSetup.Plain.NOT_SET_UP.reason(false),
            StatusText.notificationResources(status, appSeen = false, fallbackRunning = true),
        )
        status.put("route_count", 3)
        assertEquals(
            HomeSetup.Plain.CONNECTING.title to HomeSetup.Plain.CONNECTING.reason(false),
            StatusText.notificationResources(status, appSeen = false, fallbackRunning = true),
        )
    }
    @Test fun savedLinkOverridesEmptyCoreRoutes() {
        val status = JSONObject().put("running", true).put("route_count", 0).put("subscriptions", org.json.JSONArray())
        for ((state, title) in listOf("starting" to R.string.home_connecting_title, "working" to R.string.home_working_title)) {
            status.put("health", JSONObject().put("state", state))
            assertEquals(title, StatusText.notificationResources(status, false, true, hasLink = true).first)
            assertEquals(HomeSetup.Plain.NOT_SET_UP.title, StatusText.notificationResources(status, false, true, hasLink = false).first)
        }
    }

}
