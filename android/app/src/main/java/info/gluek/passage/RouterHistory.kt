package info.gluek.passage

internal fun neutralHistoryMessage(message: String): String = when {
    message.startsWith("unblocked:") -> "resumed:" + message.removePrefix("unblocked:")
    message.startsWith("stalled:") -> message.replaceFirst("), blocked for ", "), paused for ")
    else -> message
}

internal class RouterHistory {
    private val copied = linkedSetOf<Pair<String, String>>()

    fun select(events: List<Pair<String, String>>): List<Pair<String, String>> =
        events.filter { (time, message) ->
            val isHistory = message.startsWith("stalled:") || message.startsWith("resumed:") ||
                message.startsWith("unblocked:") || message.startsWith("disagree:")
            val isNew = time.isNotEmpty() && isHistory && copied.add(time to message)
            if (copied.size > 1024) copied.remove(copied.first())
            isNew
        }
}
