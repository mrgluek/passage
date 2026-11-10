package info.gluek.passage

import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

class ReportFilesTest {
    @get:Rule val temporary = TemporaryFolder()
    @Test fun namesAreTextAttachmentsWithoutPaths() {
        assertEquals("passage-report-123.txt", ReportFiles.name(123))
        assertTrue(ReportFiles.isReport(ReportFiles.name(123)))
        for (name in listOf("../passage-report-123.txt", "passage-report-a.txt", "other.txt", "passage-report-123.txt/extra")) {
            assertFalse(name, ReportFiles.isReport(name))
        }
    }
    @Test fun cleanupDeletesOnlyOldReports() {
        val directory = temporary.newFolder()
        val now = 200_000_000L
        val old = File(directory, ReportFiles.name(1)).apply { writeText("old"); setLastModified(1) }
        val recent = File(directory, ReportFiles.name(now)).apply { writeText("recent"); setLastModified(now) }
        val boundary = File(directory, ReportFiles.name(2)).apply { writeText("boundary"); setLastModified(now - 86_400_000L) }
        val other = File(directory, "other.txt").apply { writeText("other"); setLastModified(1) }
        val folder = File(directory, ReportFiles.name(3)).apply { mkdir(); setLastModified(1) }
        ReportFiles.cleanup(directory, now)
        assertFalse(old.exists())
        for (file in listOf(recent, boundary, other, folder)) assertTrue(file.exists())
    }
    @Test fun copyIsOnlyTheFallbackWhenNoShareHandlerExists() {
        assertEquals(ReportFiles.Delivery.COPY, ReportFiles.delivery(0))
        assertEquals(ReportFiles.Delivery.SHARE, ReportFiles.delivery(1))
        assertEquals(ReportFiles.Delivery.SHARE, ReportFiles.delivery(4))
    }
}
