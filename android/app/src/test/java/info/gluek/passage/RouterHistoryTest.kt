package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Test

class RouterHistoryTest {
    @Test
    fun copiesOnlyHistoryEventsOnceByKey() {
        val history = RouterHistory()
        val stall = "2026-01-01T00:00:01Z" to "stalled: relay-1 direct (first reply), paused for 2 min, count=1"
        val resume = "2026-01-01T00:00:02Z" to "resumed: relay-1 direct (first reply)"
        val events = listOf("2026-01-01T00:00:00Z" to "router started", stall, stall, resume)
        assertEquals(listOf(stall, resume), history.select(events))
        assertEquals(emptyList<Pair<String, String>>(), history.select(events))
        assertEquals(listOf(stall.first to "stalled: changed"), history.select(listOf(stall.first to "stalled: changed")))
        assertEquals(listOf("2026-01-01T00:00:03Z" to stall.second), history.select(listOf("2026-01-01T00:00:03Z" to stall.second)))
    }

    @Test
    fun keepsLegacyResumeAndUsesNeutralWording() {
        val history = RouterHistory()
        val event = "2026-01-01T00:00:01Z" to "unblocked: relay-1 direct (first reply)"
        assertEquals(listOf(event), history.select(listOf(event, event)))
        assertEquals("resumed: relay-1 direct (first reply)", neutralHistoryMessage(event.second))
        assertEquals(
            "stalled: relay-1 direct (first reply), paused for 2 min, count=1",
            neutralHistoryMessage("stalled: relay-1 direct (first reply), blocked for 2 min, count=1"),
        )
    }

    @Test
    fun copiesDisagreementOnceByKey() {
        val history = RouterHistory()
        val event = "2026-01-01T00:00:01Z" to "disagree: relay-1 probe=ok conn=stall via route-a"
        assertEquals(listOf(event), history.select(listOf(event, event)))
        assertEquals(emptyList<Pair<String, String>>(), history.select(listOf(event)))
        assertEquals(emptyList<Pair<String, String>>(), history.select(listOf("" to event.second)))
    }

    @Test
    fun copiesDistinctSimultaneousAndBackdatedEvents() {
        val history = RouterHistory()
        val first = "2026-01-01T00:00:02Z" to "stalled: relay-1 direct (first reply), paused for 2 min, count=2"
        val second = first.first to "stalled: relay-2 direct (first reply), paused for 2 min, count=2"
        val backdated = "2026-01-01T00:00:01Z" to "resumed: relay-1 direct (cooldown ended)"
        assertEquals(listOf(first, second), history.select(listOf(first, second, first)))
        assertEquals(listOf(backdated), history.select(listOf(backdated, second, backdated)))
        assertEquals(emptyList<Pair<String, String>>(), history.select(listOf("" to first.second)))
    }

    @Test
    fun boundsSavedKeys() {
        val history = RouterHistory()
        val events = (1..1025).map { "2026-01-01T00:00:01Z" to "stalled: relay-$it direct" }
        assertEquals(events, history.select(events))
        assertEquals(emptyList<Pair<String, String>>(), history.select(events.drop(1)))
        assertEquals(listOf(events.first()), history.select(listOf(events.first())))
        assertEquals(emptyList<Pair<String, String>>(), history.select(listOf(events.first(), events.last())))
    }
}
