package info.gluek.passage

import info.gluek.passage.BackgroundHint.Card
import info.gluek.passage.BackgroundHint.Vendor
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory

class BackgroundHintTest {
    @Test fun vendorByMakerOrBrand() {
        val cases = listOf(
            "Xiaomi" to "POCO" to Vendor.XIAOMI, "Xiaomi" to "Redmi" to Vendor.XIAOMI,
            "HUAWEI" to "HONOR" to Vendor.HUAWEI, "HONOR" to "Honor" to Vendor.HUAWEI,
            "OPPO" to "realme" to Vendor.OPPO, "OnePlus" to "OnePlus" to Vendor.OPPO,
            "vivo" to "iQOO" to Vendor.VIVO, "samsung" to "samsung" to Vendor.SAMSUNG,
            "TECNO" to "TECNO" to Vendor.TRANSSION, "INFINIX" to "Infinix" to Vendor.TRANSSION,
            "itel" to "itel" to Vendor.TRANSSION, "Transsion Holdings" to "TECNO" to Vendor.TRANSSION,
            "OUKITEL" to "OUKITEL" to Vendor.OTHER, "Xiaomi Communications" to "Redmi" to Vendor.XIAOMI,
            "Google" to "google" to Vendor.OTHER, "" to "" to Vendor.OTHER,
        )
        for ((pair, want) in cases) assertEquals(pair.toString(), want, BackgroundHint.vendor(pair.first, pair.second))
    }

    @Test fun cleanerStopsAreRecognised() {
        // Seen in a field report: reason 13 with status 0 and this description.
        assertTrue(BackgroundHint.stoppedByCleaner(13, "OneKeyClean"))
        assertTrue(BackgroundHint.stoppedByCleaner(10, "[FORCE STOP] stop com.example due to SwipeUpClean"))
        assertTrue(BackgroundHint.stoppedByCleaner(13, "GarbageClean"))
        assertFalse(BackgroundHint.stoppedByCleaner(13, ""))
        assertFalse(BackgroundHint.stoppedByCleaner(10, "stop com.example due to from pid 1234"))
        assertFalse(BackgroundHint.stoppedByCleaner(4, "OneKeyClean"))
        assertFalse(BackgroundHint.stoppedByCleaner(5, "crash"))
    }

    @Test fun cardRules() {
        val x = Vendor.XIAOMI
        assertEquals(Card.NONE, BackgroundHint.card(false, true, x, false, 100, 0))
        assertEquals(Card.ADVANCE, BackgroundHint.card(true, true, x, false, 0, 0))
        assertEquals(Card.NONE, BackgroundHint.card(true, false, x, false, 0, 0))
        assertEquals(Card.NONE, BackgroundHint.card(true, true, x, true, 0, 0))
        assertEquals(Card.NONE, BackgroundHint.card(true, true, Vendor.OTHER, false, 0, 0))
        // A stop shows the stronger card even when the proxy is off or the advance card was dismissed.
        assertEquals(Card.KILLED, BackgroundHint.card(true, false, x, true, 100, 0))
        assertEquals(Card.KILLED, BackgroundHint.card(true, true, Vendor.OTHER, false, 100, 50))
        // Dismissed once, a newer stop shows it again, the same stop does not.
        assertEquals(Card.NONE, BackgroundHint.card(true, true, Vendor.OTHER, true, 100, 100))
        assertEquals(Card.KILLED, BackgroundHint.card(true, true, Vendor.OTHER, true, 200, 100))
    }

    @Test fun everyVendorHasStepsInBothLanguages() {
        for (locale in listOf("values", "values-ru")) {
            val doc = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(File("src/main/res/$locale/strings.xml"))
            val names = (0 until doc.getElementsByTagName("string").length)
                .map { doc.getElementsByTagName("string").item(it).attributes.getNamedItem("name").nodeValue }.toSet()
            val keys = listOf("bg_advance_title", "bg_advance_reason", "bg_killed_title", "bg_killed_reason", "bg_open_settings", "bg_dismiss") +
                Vendor.entries.map { "bg_steps_" + it.name.lowercase() }
            for (key in keys) assertTrue("$locale/$key", key in names)
        }
    }
}
