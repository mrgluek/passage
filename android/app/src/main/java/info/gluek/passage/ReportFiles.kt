package info.gluek.passage

import android.content.Context
import android.net.Uri
import java.io.File

object ReportFiles {
    enum class Delivery { SHARE, COPY }
    fun delivery(availableHandlers: Int): Delivery = if (availableHandlers > 0) Delivery.SHARE else Delivery.COPY

    private const val MAX_AGE = 24 * 60 * 60 * 1000L
    fun name(timestamp: Long): String = "passage-report-$timestamp.txt"
    fun isReport(name: String): Boolean = Regex("passage-report-[0-9]+\\.txt").matches(name)
    fun cleanup(directory: File, now: Long) {
        directory.listFiles()?.filter { it.isFile && isReport(it.name) && now - it.lastModified() > MAX_AGE }
            ?.forEach { it.delete() }
    }
    fun write(context: Context, report: String): Uri {
        val directory = File(context.cacheDir, "reports").apply { mkdirs() }
        val now = System.currentTimeMillis()
        cleanup(directory, now)
        var stamp = now
        var file = File(directory, name(stamp))
        while (!file.createNewFile()) { file = File(directory, name(++stamp)) }
        file.writeText(report, Charsets.UTF_8)
        return Uri.Builder().scheme("content").authority("${context.packageName}.reports").appendPath(file.name).build()
    }
}
