package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test

class HomeConnectTest {
    @Test fun clipboardMarksOnlyProxyLinksWithCredentials() {
        assertTrue(HomeSetup.isCredentialLink("socks5://name:password@example.org:1080"))
        assertTrue(HomeSetup.isCredentialLink("tg://socks?server=example.org&port=1080&user=name&pass=password"))
        assertTrue(HomeSetup.isCredentialLink(Settings.veltaLink("example.org", 1080, "name", "password")))
        assertFalse(HomeSetup.isCredentialLink("socks5://example.org:1080"))
        assertFalse(HomeSetup.isCredentialLink("tg://socks?server=example.org&port=1080"))
        assertFalse(HomeSetup.isCredentialLink(Settings.veltaLink("example.org", 1080)))
    }

    @Test fun deltaChatFamilyGetsOnlyTheSocksLinkAndInstructions() {
        val content = HomeSetup.connect(HomeSetup.AppTab.DELTA_CHAT, "socks-link", "telegram-link", "velta-link")
        assertEquals(R.string.home_delta_how, content.instruction)
        assertEquals(R.string.copy_proxy_link, content.action)
        assertEquals("socks-link", content.clipboard)
        assertNull(content.open)
    }

    @Test fun telegramCopiesExactlyTheLinkItOpens() {
        val content = HomeSetup.connect(HomeSetup.AppTab.TELEGRAM, "socks-link", "telegram-link", "velta-link")
        assertEquals(R.string.home_telegram_how, content.instruction)
        assertEquals(R.string.home_telegram_open, content.action)
        assertEquals("telegram-link", content.clipboard)
        assertEquals(content.clipboard, content.open)
    }

    @Test fun otherAppUsesManualValuesWithoutLinkActions() {
        val content = HomeSetup.connect(HomeSetup.AppTab.OTHER, "socks-link", "telegram-link", "velta-link")
        assertNull(content.download)
        assertEquals(R.string.home_other_how, content.instruction)
        assertNull(content.action)
        assertNull(content.clipboard)
        assertNull(content.open)
    }
    @Test fun veltaCopiesAndOpensItsOwnFormat() {
        val content = HomeSetup.connect(HomeSetup.AppTab.VELTA, "socks-link", "telegram-link", "velta-link")
        assertEquals(R.string.home_velta_how, content.instruction)
        assertEquals(R.string.home_velta_open, content.action)
        assertEquals("velta-link", content.clipboard)
        assertEquals(content.clipboard, content.open)
        assertEquals(listOf(HomeSetup.AppTab.DELTA_CHAT, HomeSetup.AppTab.VELTA, HomeSetup.AppTab.TELEGRAM, HomeSetup.AppTab.OTHER), HomeSetup.AppTab.entries)
    }
    @Test fun tabsUseTwoRowsForLargeFontsOrNarrowLabels() {
        assertEquals(4, HomeSetup.appTabColumns(1f, 288, 60))
        assertEquals(2, HomeSetup.appTabColumns(1.5f, 288, 60))
        assertEquals(2, HomeSetup.appTabColumns(1f, 200, 60))
    }
    @Test fun tabsExposeOnlyTheRequestedInstructionsActionsAndDownloads() {
        val delta = HomeSetup.connect(HomeSetup.AppTab.DELTA_CHAT, "socks-link", "telegram-link", "velta-link")
        assertNotNull(delta.instruction)
        assertEquals(R.string.copy_proxy_link, delta.action)
        assertEquals(HomeSetup.DELTA_CHAT_DOWNLOAD, delta.download)
        val velta = HomeSetup.connect(HomeSetup.AppTab.VELTA, "socks-link", "telegram-link", "velta-link")
        assertNotNull(velta.instruction)
        assertEquals(R.string.home_velta_open, velta.action)
        assertEquals("https://velta.cc/#download", velta.download)
        val telegram = HomeSetup.connect(HomeSetup.AppTab.TELEGRAM, "socks-link", "telegram-link", "velta-link")
        assertNotNull(telegram.instruction)
        assertEquals(R.string.home_telegram_open, telegram.action)
        assertNull(telegram.download)
        val other = HomeSetup.connect(HomeSetup.AppTab.OTHER, "socks-link", "telegram-link", "velta-link")
        assertEquals(R.string.home_other_how, other.instruction)
        assertNull(other.action)
        assertNull(other.download)
    }
}
