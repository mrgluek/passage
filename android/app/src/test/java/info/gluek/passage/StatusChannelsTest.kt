package info.gluek.passage

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Test

class StatusChannelsTest {
    private val now = 2_000_000_000L
    private fun labels(locale: String): StatusChannels.Labels {
        val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder()
            .parse(File("src/main/res/$locale/strings.xml")).getElementsByTagName("string")
        val strings = (0 until nodes.length).associate { i ->
            val node = nodes.item(i)
            node.attributes.getNamedItem("name").nodeValue to node.textContent.removeSurrounding("\"")
        }
        fun text(key: String) = strings.getValue(key)
        return StatusChannels.Labels(text("channel_mail"), text("channel_telegram"), text("channel_other"),
            text("channel_shared"), text("route_direct"), text("channel_smart"),
            listOf("channel_tunnel", "channel_tunnel_type", "channel_tunnel_id", "channel_tunnel_plain").map(::text),
            text("channel_whatsapp"), text("channel_signal"), text("channel_and"), text("route_tunnel"), text("channel_my"))
    }
    private val en = labels("values")
    private val ru = labels("values-ru")

    @Test fun channelLabels() {
        assertEquals(listOf("Mail", "Tg", "WA", "Molly", "Other", "smart", " and "),
            listOf(en.mail, en.telegram, en.whatsapp, en.signal, en.other, en.smart, en.and))
        assertEquals(listOf("Почта", "Tg", "WA", "Molly", "Другое", "с подстройкой", " и "),
            listOf(ru.mail, ru.telegram, ru.whatsapp, ru.signal, ru.other, ru.smart, ru.and))
    }

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
                Case("own route", listOf(mail.copy(route = "tunnel", id = "my", via = "my")), "${l.mail}: ${l.mine}"),
                Case("type", listOf(mail.copy(route = "tunnel", via = "iroh")), "${l.mail}: ${l.tunnel[1].format("iroh")}"),
            )
            for (c in cases) assertEquals(c.name, c.expected, StatusChannels.lines(c.groups, c.mode, "direct", "", l, now, c.shown).text)
        }
    }

    @Test fun appChannelForms() {
        for (l in listOf(en, ru)) {
            val mail = StatusChannels.Group("a", "relay", "direct", lastConn = now)
            val tg = StatusChannels.Group("b", "telegram", "tunnel", "ya", lastConn = now)
            val wa = StatusChannels.Group("g.whatsapp.net", "relay", "tunnel", "berry", "iroh", now, app = "whatsapp")
            val molly = StatusChannels.Group("chat.signal.org", "relay", "tunnel", "br", lastConn = now, app = "signal")
            val smart = mail.copy(name = "c", route = "smart")
            val own = mail.copy(route = "tunnel", id = "my", via = "my")
            val cases = listOf(
                listOf(wa) to "WA: ${l.tunnel[0].format("iroh", "berry")}",
                listOf(molly) to "Molly: ${l.tunnel[2].format("br")}",
                listOf(mail.copy(kind = "other")) to "${l.other}: ${l.direct}",
                listOf(mail, wa) to "${l.mail}: ${l.direct}, WA: ${l.tunnel[0].format("iroh", "berry")}",
                listOf(mail, tg, wa) to "${l.mail}: ${l.direct}, Tg: ya, WA: berry",
                listOf(mail, tg, wa, molly) to "${l.mail}: ${l.direct}, Tg: ya, WA: berry, Molly: br",
                listOf(mail, smart, mail.copy(name = "d")) to "${l.mail}: ${l.direct}${l.and}${l.smart}",
                listOf(mail, smart, tg, wa) to "${l.mail}: ${l.direct}${l.and}${l.smart}, Tg: ya, WA: berry",
                listOf(own) to "${l.mail}: ${l.mine}",
                listOf(own, tg, wa) to "${l.mail}: my, Tg: ya, WA: berry",
            )
            for ((groups, expected) in cases) assertEquals(expected, StatusChannels.lines(groups, "auto", "direct", "", l, now).text)
            for (via in listOf("iroh", "http", "soar", "")) {
                assertEquals("${l.mail}: ${l.direct}, Tg: ya, WA: ${via.ifEmpty { l.tunnelShort }}",
                    StatusChannels.lines(listOf(mail, tg, wa.copy(id = "", via = via)), "auto", "direct", "", l, now).text)
            }
        }
    }

    @Test fun appChannelTiming() {
        for (l in listOf(en, ru)) for (app in listOf("", "whatsapp", "signal")) {
            val g = StatusChannels.Group("a", "relay", "direct", lastConn = now - 960_000, app = app)
            val shared = "${l.shared}: ${l.direct}"
            for (elapsed in listOf(240_000L, 300_000L)) {
                val result = StatusChannels.lines(listOf(g), "auto", "direct", "", l, now, mapOf("a" to now - elapsed))
                assertEquals(elapsed < 300_000, result.text != shared)
                assertEquals(elapsed < 300_000, result.shown.isNotEmpty())
            }
            val d = StatusChannels.Display()
            assertEquals(shared, d.sample(emptyList(), "auto", "direct", "", l, now))
            val groups = listOf(g.copy(lastConn = now))
            assertEquals(shared, d.sample(groups, "auto", "direct", "", l, now + 1000))
            assertEquals(shared, d.sample(groups, "auto", "direct", "", l, now + 10_000))
            assertEquals(StatusChannels.lines(groups, "auto", "direct", "", l, now).text,
                d.sample(groups, "auto", "direct", "", l, now + 11_000))
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
