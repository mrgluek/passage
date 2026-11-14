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

    @Test fun homeSetupAndStatusCopyAreLocalized() {
        for ((locale, working) in listOf("values" to "Proxy works", "values-ru" to "Прокси работает")) {
            val document = DocumentBuilderFactory.newInstance().newDocumentBuilder()
                .parse(File("src/main/res/$locale/strings.xml"))
            val nodes = document.getElementsByTagName("string")
            val strings = (0 until nodes.length).associate {
                val node = nodes.item(it)
                node.attributes.getNamedItem("name").nodeValue to node.textContent
            }
            assertEquals(working, strings["home_working_title"])
            for ((key, value) in strings.filterKeys { it.startsWith("home_") && it.endsWith("_title") && it !in listOf("home_battery_title", "home_add_title", "home_setup_title") }) {
                assertFalse("$locale/$key", value.contains("Passage:"))
                assertFalse("$locale/$key", value.first().isLowerCase())
            }
            for (key in listOf("home_velta_how", "home_molly_how", "allow_no_password", "allow_no_password_help")) {
                assertFalse("$locale/$key", strings[key].isNullOrBlank())
            }
            assertFalse(strings.containsKey("home_tab_velta"))
            assertFalse(strings.containsKey("status_title"))
        }
    }

    @Test fun otherTabSharesTheSettingsAuthenticationPath() {
        val main = File("src/main/java/info/gluek/passage/MainActivity.kt").readText()
        val settings = File("src/main/java/info/gluek/passage/SettingsActivity.kt").readText()
        org.junit.Assert.assertTrue(main.contains("getString(R.string.home_molly_how)"))
        org.junit.Assert.assertTrue(main.contains("getString(R.string.proxy_host) to \"localhost\""))
        org.junit.Assert.assertTrue(main.contains("isChecked = !settings.auth"))
        org.junit.Assert.assertTrue(main.contains("settings.setAuthentication(this@MainActivity, !on)"))
        org.junit.Assert.assertTrue(settings.contains("settings.setAuthentication(this@SettingsActivity, on)"))
        org.junit.Assert.assertTrue(main.indexOf("getString(R.string.home_molly_how)") < main.indexOf("getString(R.string.home_molly_other_apps)"))
    }
}
