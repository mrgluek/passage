package info.gluek.passage

/** Pure rules for the update card and the Updates settings: what to show and which button. */
object UpdateFlow {
    enum class Stage { IDLE, CHECKING, AVAILABLE, DOWNLOADING, VERIFYING, READY, FAILED, LATEST }

    enum class Action { NONE, DOWNLOAD, CANCEL, INSTALL }

    /** What [Updater] knows now; [available] is the newer version, if any. */
    data class Snapshot(
        val checking: Boolean = false,
        val downloading: Boolean = false,
        val verifying: Boolean = false,
        val ready: Boolean = false,
        val failed: Boolean = false,
        val checked: Boolean = false,
        val available: String? = null,
    )

    fun stage(s: Snapshot): Stage = when {
        s.checking -> Stage.CHECKING
        s.downloading -> if (s.verifying) Stage.VERIFYING else Stage.DOWNLOADING
        s.failed -> Stage.FAILED
        s.ready -> Stage.READY
        s.available != null -> Stage.AVAILABLE
        s.checked -> Stage.LATEST
        else -> Stage.IDLE
    }

    /** The one button that moves the update on; after a failure it retries the step that failed. */
    fun action(s: Snapshot): Action = when {
        s.downloading -> Action.CANCEL
        s.checking -> Action.NONE
        s.ready -> Action.INSTALL
        s.available != null -> Action.DOWNLOAD
        else -> Action.NONE
    }

    /** "Not now" fits only while nothing runs. */
    fun canDismiss(s: Snapshot): Boolean = s.available != null && !s.checking && !s.downloading

    /**
     * The main screen shows the card while a newer version exists, unless the
     * user put this version aside; a running download always stays visible.
     * Once the newest version is installed, [Snapshot.available] is null.
     */
    fun homeVisible(s: Snapshot, dismissed: String): Boolean =
        s.available != null && (s.downloading || dismissed != s.available)

    /** Download progress in tenths of a percent, or -1 while the size is unknown. */
    fun permille(done: Long, total: Long): Int =
        if (total <= 0) -1 else (done.coerceIn(0, total) * 1000 / total).toInt()
}
