package info.gluek.passage

import org.junit.Assert.assertEquals
import org.junit.Test

class StatusChannelsTest {
    private val now = 2_000_000_000L
    private val en = StatusChannels.Labels("Mail", "Tg", "Other", "Route", "direct", "smart", listOf("via %s tunnel (%s)", "via %s tunnel", "via tunnel (%s)", "via tunnel"))
    private val ru = StatusChannels.Labels("Почта", "Tg", "Другое", "Маршрут", "напрямую", "smart (с подстройкой)", listOf("через %s-туннель (%s)", "через %s-туннель", "через туннель (%s)", "через туннель"))

    @Test fun channelLines() {
        data class Case(val name: String, val groups: List<StatusChannels.Group>, val expected: String, val mode: String = "auto", val shown: Map<String, Long> = emptyMap())
        for (l in listOf(en, ru)) {
            val mail = StatusChannels.Group("mail", "relay", "direct", lastConn = now - 60_000)
            val tg = StatusChannels.Group("tg", "telegram", "tunnel", "berry", lastConn = now - 960_000)
            val cases = listOf(
                Case("shared", emptyList(), "${l.shared}: ${l.direct}"),
                Case("direct", listOf(mail), "${l.mail}: ${l.direct}"),
                Case("id", listOf(mail.copy(route = "tunnel", id = "berry", via = "iroh")), "${l.mail}: ${l.tunnel[0].format("iroh", "berry")}"),
                Case("smart", listOf(mail.copy(route = "smart")), "${l.mail}: ${l.smart}"),
                Case("expired", listOf(mail.copy(lastConn = now - 960_000)), "${l.shared}: ${l.direct}"),
                Case("minimum", listOf(mail.copy(lastConn = now - 960_000)), "${l.mail}: ${l.direct}", shown = mapOf("mail" to now - 240_000)),
                Case("minimum expired", listOf(mail.copy(lastConn = now - 960_000)), "${l.shared}: ${l.direct}", shown = mapOf("mail" to now - 300_000)),
                Case("telegram only", listOf(tg), "Tg: ${l.tunnel[2].format("berry")}"),
                Case("unused telegram", listOf(tg.copy(lastConn = 0)), "${l.shared}: ${l.direct}"),
                Case("several", listOf(mail, tg), "${l.mail}: ${l.direct}, Tg: ${l.tunnel[2].format("berry")}"),
                Case("pinned", listOf(mail.copy(route = "tunnel")), "${l.mail}: ${l.tunnel[2].format("berry")}", mode = "route:berry"),
                Case("type", listOf(mail.copy(route = "tunnel", via = "iroh")), "${l.mail}: ${l.tunnel[1].format("iroh")}"),
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
        assertEquals("Route: via tunnel", sample("tunnel", 25))
    }

    @Test fun noRouteYetGivesNoLine() {
        assertEquals("", StatusChannels.lines(emptyList(), "auto", "", "", en, now).text)
    }

    @Test fun detailShowsChannelsOnlyWhileWorking() {
        val lines = "Mail: berry"
        assertEquals(lines, HomeSetup.Plain.WORKING.detail("ok", lines))
        assertEquals("backup $lines", HomeSetup.Plain.LIMITED.detail("backup", lines))
        for (p in listOf(HomeSetup.Plain.CONNECTING, HomeSetup.Plain.SLOW, HomeSetup.Plain.NO_INTERNET, HomeSetup.Plain.READY, HomeSetup.Plain.OFF)) {
            assertEquals(p.name, "reason", p.detail("reason", lines))
        }
        assertEquals("reason", HomeSetup.Plain.WORKING.detail("reason", ""))
    }
}
