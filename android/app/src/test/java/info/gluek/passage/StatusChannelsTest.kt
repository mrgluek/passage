package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Test

class StatusChannelsTest {
    private val now = 2_000_000_000L
    private val en = StatusChannels.Labels("Mail", "Tg", "Other", "Route", "direct", "smart")
    private val ru = StatusChannels.Labels("Почта", "Tg", "Другое", "Маршрут", "напрямую", "smart (с подстройкой)")

    @Test fun channelLines() {
        data class Case(val name: String, val groups: List<StatusChannels.Group>, val expected: String, val mode: String = "auto", val shown: Map<String, Long> = emptyMap())
        for (l in listOf(en, ru)) {
            val mail = StatusChannels.Group("mail", "relay", "direct", lastConn = now - 60_000)
            val tg = StatusChannels.Group("tg", "telegram", "tunnel", "berry", lastConn = now - 960_000)
            val cases = listOf(
                Case("shared", emptyList(), "${l.shared}: ${l.direct}"),
                Case("direct", listOf(mail), "${l.mail}: ${l.direct}"),
                Case("id", listOf(mail.copy(route = "tunnel", id = "berry", via = "iroh")), "${l.mail}: berry"),
                Case("smart", listOf(mail.copy(route = "smart")), "${l.mail}: ${l.smart}"),
                Case("expired", listOf(mail.copy(lastConn = now - 960_000)), "${l.shared}: ${l.direct}"),
                Case("minimum", listOf(mail.copy(lastConn = now - 960_000)), "${l.mail}: ${l.direct}", shown = mapOf("mail" to now - 240_000)),
                Case("minimum expired", listOf(mail.copy(lastConn = now - 960_000)), "${l.shared}: ${l.direct}", shown = mapOf("mail" to now - 300_000)),
                Case("telegram only", listOf(tg), "Tg: berry"),
                Case("unused telegram", listOf(tg.copy(lastConn = 0)), "${l.shared}: ${l.direct}"),
                Case("several", listOf(mail, tg), "${l.mail}: ${l.direct}, Tg: berry"),
                Case("pinned", listOf(mail.copy(route = "tunnel")), "${l.mail}: berry", mode = "route:berry"),
                Case("type", listOf(mail.copy(route = "tunnel", via = "iroh")), "${l.mail}: iroh"),
            )
            for (c in cases) assertEquals(c.name, c.expected, StatusChannels.lines(c.groups, c.mode, "direct", "", l, now, c.shown).text)
        }
    }

    @Test fun waitsForStableRoute() {
        val d = StatusChannels.Display()
        fun sample(route: String, seconds: Long) = d.sample(emptyList(), "auto", route, "", en, now + seconds * 1000)
        assertEquals("Route: direct", sample("direct", 0))
        assertEquals("Route: direct", sample("smart", 1))
        assertEquals("Route: direct", sample("smart", 10))
        assertEquals("Route: smart", sample("smart", 11))
        assertEquals("Route: smart", sample("direct", 12))
        assertEquals("Route: smart", sample("tunnel", 15))
        assertEquals("Route: smart", sample("tunnel", 24))
        assertEquals("Route: tunnel", sample("tunnel", 25))
    }
}
