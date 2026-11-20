package info.gluek.passage

import info.gluek.passage.UpdateFlow.Action
import info.gluek.passage.UpdateFlow.Snapshot
import info.gluek.passage.UpdateFlow.Stage
import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateFlowTest {
    private val offered = Snapshot(available = "1.2.0")

    @Test fun stagesFollowTheUpdater() {
        assertEquals(Stage.IDLE, UpdateFlow.stage(Snapshot()))
        assertEquals(Stage.LATEST, UpdateFlow.stage(Snapshot(checked = true)))
        assertEquals(Stage.CHECKING, UpdateFlow.stage(Snapshot(checking = true)))
        assertEquals(Stage.AVAILABLE, UpdateFlow.stage(offered))
        assertEquals(Stage.DOWNLOADING, UpdateFlow.stage(offered.copy(downloading = true)))
        assertEquals(Stage.VERIFYING, UpdateFlow.stage(offered.copy(downloading = true, verifying = true)))
        assertEquals(Stage.READY, UpdateFlow.stage(offered.copy(ready = true)))
        assertEquals(Stage.FAILED, UpdateFlow.stage(offered.copy(failed = true)))
        assertEquals(Stage.FAILED, UpdateFlow.stage(offered.copy(failed = true, ready = true)))
        // A new attempt hides the old failure.
        assertEquals(Stage.DOWNLOADING, UpdateFlow.stage(offered.copy(failed = true, downloading = true)))
    }

    @Test fun oneButtonMovesTheUpdateOn() {
        assertEquals(Action.NONE, UpdateFlow.action(Snapshot()))
        assertEquals(Action.NONE, UpdateFlow.action(offered.copy(checking = true)))
        assertEquals(Action.DOWNLOAD, UpdateFlow.action(offered))
        assertEquals(Action.CANCEL, UpdateFlow.action(offered.copy(downloading = true)))
        assertEquals(Action.INSTALL, UpdateFlow.action(offered.copy(ready = true)))
        // Retry repeats the step that failed: the download, or the install of a verified file.
        assertEquals(Action.DOWNLOAD, UpdateFlow.action(offered.copy(failed = true)))
        assertEquals(Action.INSTALL, UpdateFlow.action(offered.copy(failed = true, ready = true)))
        assertEquals(Action.NONE, UpdateFlow.action(Snapshot(failed = true, checked = true)))
    }

    @Test fun homeCardShowsOnlyForANewerVersionNotPutAside() {
        assertFalse(UpdateFlow.homeVisible(Snapshot(), ""))
        assertFalse(UpdateFlow.homeVisible(Snapshot(checked = true), ""))
        assertTrue(UpdateFlow.homeVisible(offered, ""))
        assertFalse(UpdateFlow.homeVisible(offered, "1.2.0"))
        assertTrue(UpdateFlow.homeVisible(offered.copy(downloading = true), "1.2.0"))
        // Putting one version aside does not hide a later one.
        assertTrue(UpdateFlow.homeVisible(Snapshot(available = "1.3.0"), "1.2.0"))
        assertTrue(UpdateFlow.canDismiss(offered))
        assertTrue(UpdateFlow.canDismiss(offered.copy(ready = true)))
        assertFalse(UpdateFlow.canDismiss(offered.copy(downloading = true)))
        assertFalse(UpdateFlow.canDismiss(Snapshot()))
    }

    @Test fun progressIsClamped() {
        assertEquals(-1, UpdateFlow.permille(10, 0))
        assertEquals(0, UpdateFlow.permille(0, 100))
        assertEquals(500, UpdateFlow.permille(50, 100))
        assertEquals(1000, UpdateFlow.permille(150, 100))
        assertEquals(0, UpdateFlow.permille(-5, 100))
        assertEquals(333, UpdateFlow.permille(1L shl 32, 3L shl 32))
    }

    @Test fun updateStringsMatchInBothLanguages() {
        fun strings(locale: String): Map<String, String> {
            val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder()
                .parse(File("src/main/res/$locale/strings.xml")).getElementsByTagName("string")
            return (0 until nodes.length).associate {
                nodes.item(it).attributes.getNamedItem("name").nodeValue to nodes.item(it).textContent
            }.filterKeys { it.startsWith("update") }
        }
        val en = strings("values")
        val ru = strings("values-ru")
        assertEquals(en.keys, ru.keys)
        for (key in listOf("update_download_size", "update_home_text", "update_home_downloading",
            "update_verifying", "update_home_ready", "update_skip")) {
            assertTrue(key, en.containsKey(key))
        }
        val args = Regex("%\\d\\$[sd]")
        for ((key, value) in en) {
            assertEquals(key, args.findAll(value).map { it.value }.toSet(), args.findAll(ru.getValue(key)).map { it.value }.toSet())
            assertFalse(key, ru.getValue(key).isBlank())
        }
    }

    @Test fun mainScreenRunsTheUpdateItself() {
        val main = File("src/main/java/info/gluek/passage/MainActivity.kt").readText()
        val updater = File("src/main/java/info/gluek/passage/Updater.kt").readText()
        assertTrue(main.contains("UpdateSection(this, home = true)"))
        assertFalse(main.contains("SettingsActivity::class.java).setAction(Updater.ACTION_SHOW)"))
        assertTrue(updater.contains("Intent(context, MainActivity::class.java).setAction(ACTION_SHOW)"))
    }
}
