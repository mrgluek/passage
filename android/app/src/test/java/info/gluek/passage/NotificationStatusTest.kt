package info.gluek.passage

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
}
