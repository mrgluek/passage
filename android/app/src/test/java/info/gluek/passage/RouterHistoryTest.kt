package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Test

class RouterHistoryTest {
    @Test
    fun copiesOnlyStallEventsOnceByTimestamp() {
        val history = RouterHistory()
        val stall = "2026-01-01T00:00:01Z" to "stalled: direct (first reply), blocked for 2 min, count=1"
        val unblock = "2026-01-01T00:00:02Z" to "unblocked: direct (first reply)"
        val events = listOf("2026-01-01T00:00:00Z" to "router started", stall, stall, unblock)
        assertEquals(listOf(stall, unblock), history.select(events))
        assertEquals(emptyList<Pair<String, String>>(), history.select(events))
        assertEquals(emptyList<Pair<String, String>>(), history.select(listOf(stall.first to "stalled: changed")))
        assertEquals(listOf("2026-01-01T00:00:03Z" to stall.second), history.select(listOf("2026-01-01T00:00:03Z" to stall.second)))
    }
}
