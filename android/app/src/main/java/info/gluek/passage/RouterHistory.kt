package info.gluek.passage

internal class RouterHistory {
    private val copied = linkedSetOf<String>()

    fun select(events: List<Pair<String, String>>): List<Pair<String, String>> =
        events.filter { (time, message) ->
            time.isNotEmpty() && (message.startsWith("stalled:") || message.startsWith("unblocked:")) && copied.add(time)
        }.also {
            while (copied.size > 1024) copied.remove(copied.first())
        }
}
