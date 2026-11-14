package info.gluek.passage

import java.io.File
import java.util.TimeZone
import javax.xml.parsers.DocumentBuilderFactory
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class WhatsAppTest {
    @Test fun configAddsOnlyTheTwoForwardsWhenEnabled() {
        val config = JSONObject().put("listen", "example.org:1080")
        WhatsApp.configure(config, true, 20001, 20002)
        val rows = config.getJSONArray("forwards")
        assertEquals(2, rows.length())
        for ((i, name) in listOf("chat", "media").withIndex()) {
            assertEquals("whatsapp-$name", rows.getJSONObject(i).getString("name"))
            assertEquals("${Settings.HOST}:${20001 + i}", rows.getJSONObject(i).getString("listen"))
        }
        assertEquals("g.whatsapp.net:5222", rows.getJSONObject(0).getString("target"))
        assertEquals("whatsapp.net:443", rows.getJSONObject(1).getString("target"))
        WhatsApp.configure(config, false, 0, 0)
        assertFalse(config.has("forwards"))
        assertEquals("example.org:1080", config.getString("listen"))
    }

    @Test fun portsWrapStayDistinctAndSkipOccupiedPorts() {
        val tested = mutableListOf<Int>()
        val chat = WhatsApp.choosePort(setOf(32767), 12767) { tested += it; it != 20000 }
        val media = WhatsApp.choosePort(setOf(32767, chat), 12767) { true }
        assertEquals(20001, chat)
        assertEquals(20000, media)
        assertTrue(chat in 20000..32767 && media in 20000..32767)
        assertNotEquals(chat, media)
        assertNotEquals(32767, chat)
        assertNotEquals(32767, media)
        assertEquals(listOf(20000, 20001), tested)
    }

    @Test(expected = IllegalStateException::class)
    fun occupiedPortsFailInsteadOfChoosingAnUnavailablePort() {
        WhatsApp.choosePort(emptySet(), 0) { false }
    }

    @Test fun noFreePortLeavesControlsUnavailable() {
        assertNull(WhatsAppUi.availablePorts {
            WhatsApp.choosePort(emptySet(), 0) { false } to 20001
        })
        assertEquals(20001 to 20002, WhatsAppUi.availablePorts { 20001 to 20002 })
    }

    private val labels = WhatsApp.Labels("Chat", "Media", "waiting", "last connection %1\$s", "listener unavailable")

    @Test fun statusShowsWaitingAndLastConnectionAndShortListenerErrors() {
        val rows = JSONArray().put(JSONObject().put("name", "whatsapp-chat").put("up", true))
            .put(JSONObject().put("name", "whatsapp-media").put("up", false).put("error", "port busy\nextra detail")
                .put("last_conn", "2026-11-14T12:34:56.123Z"))
        val status = WhatsApp.status(rows, labels, TimeZone.getTimeZone("UTC"))
        assertEquals(listOf("Chat: waiting", "Media: last connection 12:34"), status.lines)
        assertEquals(listOf("Media: listener unavailable (port busy)"), status.warnings)
        assertTrue(status.connected)
    }

    @Test fun downWithoutErrorAndUnrelatedForwardsAreHandled() {
        val rows = JSONArray().put(JSONObject().put("name", "unrelated").put("up", false).put("error", "ignored"))
            .put(JSONObject().put("name", "whatsapp-chat").put("up", false))
        val status = WhatsApp.status(rows, labels)
        assertEquals(listOf("Chat: listener unavailable"), status.warnings)
        assertFalse(status.connected)
        assertEquals(listOf("Chat: waiting", "Media: waiting"), WhatsApp.status(null, labels).lines)
    }

    @Test fun noConnectionDoesNotTreatTheZeroTimeAsAConnection() {
        val rows = JSONArray().put(JSONObject().put("name", "whatsapp-chat").put("up", true)
            .put("last_conn", "0001-01-01T00:00:00Z"))
        val status = WhatsApp.status(rows, labels)
        assertEquals(listOf("Chat: waiting", "Media: waiting"), status.lines)
        assertTrue(status.warnings.isEmpty())
        assertFalse(status.connected)
        assertFalse(WhatsApp.showHint(true, true, false, 29_999))
        assertTrue(WhatsApp.showHint(true, true, false, 30_000))
        assertFalse(WhatsApp.showHint(false, true, false, 30_000))
        assertFalse(WhatsApp.showHint(true, false, false, 30_000))
        assertFalse(WhatsApp.showHint(true, true, true, 30_000))
    }

    @Test fun stringsExistInBothLanguagesWithMatchingPlaceholders() {
        fun strings(locale: String): Map<String, String> {
            val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder()
                .parse(File("src/main/res/$locale/strings.xml")).getElementsByTagName("string")
            return (0 until nodes.length).map { nodes.item(it) }.filter {
                it.attributes.getNamedItem("name").nodeValue.startsWith("whatsapp")
            }.associate { it.attributes.getNamedItem("name").nodeValue to it.textContent }
        }
        val english = strings("values")
        val russian = strings("values-ru")
        assertEquals(15, english.size)
        assertEquals(english.keys, russian.keys)
        for ((key, value) in english) {
            assertTrue(russian.getValue(key).isNotBlank())
            assertEquals(value.count { it == '%' }, russian.getValue(key).count { it == '%' })
        }
        assertEquals("WhatsApp", english.getValue("whatsapp"))
        assertEquals("WhatsApp", russian.getValue("whatsapp"))
    }
}
