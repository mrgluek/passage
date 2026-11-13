package info.gluek.passage

object StatusChannels {
    data class Group(val name: String, val kind: String, val route: String, val id: String = "", val via: String = "", val lastConn: Long = 0, val targets: Boolean = false)
    data class Labels(val mail: String, val telegram: String, val other: String, val shared: String, val direct: String, val smart: String, val tunnel: List<String>)
    // tunnel: formats for type and id, type only, id only, neither
    data class Lines(val text: String, val shown: Map<String, Long>)

    fun route(mode: String, route: String, id: String, via: String, labels: Labels): String = when (route) {
        "direct" -> labels.direct
        "smart" -> labels.smart
        else -> {
            val name = if (mode.startsWith("route:")) mode.removePrefix("route:") else id
            when {
                route.isEmpty() && name.isEmpty() && via.isEmpty() -> ""
                name.isNotEmpty() && via.isNotEmpty() -> labels.tunnel[0].format(via, name)
                via.isNotEmpty() -> labels.tunnel[1].format(via)
                name.isNotEmpty() -> labels.tunnel[2].format(name)
                else -> labels.tunnel[3]
            }
        }
    }

    fun lines(groups: List<Group>, mode: String, shared: String, via: String, labels: Labels, now: Long, previous: Map<String, Long> = emptyMap()): Lines {
        val shown = linkedMapOf<String, Long>()
        val kinds = linkedMapOf<String, MutableList<String>>()
        for (g in groups.sortedBy { it.name }) {
            val first = previous[g.name]
            val used = g.lastConn > 0
            val visible = (if (g.kind == "telegram") used || g.targets else used && now - g.lastConn <= 900_000) ||
                (first != null && now - first < 300_000)
            if (!visible) continue
            shown[g.name] = first ?: now
            val value = route(mode, g.route, g.id, g.via.ifEmpty { via }, labels)
            val values = kinds.getOrPut(g.kind) { mutableListOf() }
            if (value !in values) values += value
        }
        val parts = listOf("relay" to labels.mail, "telegram" to labels.telegram, "other" to labels.other).mapNotNull { (kind, label) ->
            kinds[kind]?.let { "$label: ${it.joinToString("/")}" }
        }
        val text = parts.joinToString(", ").ifEmpty {
            route(mode, shared, "", via, labels).takeIf { it.isNotEmpty() }?.let { "${labels.shared}: $it" }.orEmpty()
        }
        return Lines(text, shown)
    }

    class Display {
        private var shown = emptyMap<String, Long>()
        private var value = ""
        private var pending = ""
        private var since = 0L

        fun sample(groups: List<Group>, mode: String, shared: String, via: String, labels: Labels, now: Long): String {
            val next = lines(groups, mode, shared, via, labels, now, shown)
            shown = next.shown
            if (value.isEmpty()) value = next.text
            if (pending != next.text) { pending = next.text; since = now }
            if (now - since >= 10_000) value = next.text
            return value
        }
    }
}
