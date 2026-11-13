package info.gluek.passage

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class SettingsStringsTest {
    @Test fun emptySectionsAndLinkHintAreLocalized() {
        val keys = listOf("saved_routes_empty", "subscriptions_empty", "link_hint", "sub_takeover")
        val expected = mapOf(
            "values" to listOf(
                "No routes yet. Paste a link above to add routes.",
                "No subscriptions. A subscription link keeps your routes up to date by itself: paste it above, and Passage loads the current routes and refreshes them in the background.",
                "Paste a Passage link to add or replace routes",
                "Routes with the same names will be kept up to date by this subscription."
            ),
            "values-ru" to listOf(
                "Маршрутов пока нет. Вставьте ссылку выше, чтобы добавить маршруты.",
                "Подписок нет. Ссылка-подписка сама обновляет маршруты: вставьте её выше, и Passage загрузит актуальные маршруты и будет обновлять их в фоне.",
                "Вставьте ссылку Passage, чтобы добавить или заменить маршруты",
                "Маршруты с такими же именами будут обновляться подпиской."
            )
        )
        for ((locale, values) in expected) {
            val document = DocumentBuilderFactory.newInstance().newDocumentBuilder()
                .parse(File("src/main/res/$locale/strings.xml"))
            val strings = document.getElementsByTagName("string")
            for ((index, key) in keys.withIndex()) {
                val element = (0 until strings.length).map { strings.item(it) }
                    .single { it.attributes.getNamedItem("name").nodeValue == key }
                assertEquals("$locale/$key", values[index], element.textContent)
                assertFalse(element.attributes.getNamedItem("translatable")?.nodeValue == "false")
            }
        }
    }
}
