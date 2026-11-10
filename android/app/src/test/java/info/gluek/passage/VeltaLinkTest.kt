package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test

class VeltaLinkTest {
    @Test fun rfc4648VectorsHaveNoPadding() {
        for ((plain, encoded) in listOf("" to "", "f" to "MY", "fo" to "MZXQ", "foo" to "MZXW6", "foob" to "MZXW6YQ", "fooba" to "MZXW6YTB", "foobar" to "MZXW6YTBOI")) {
            assertEquals(encoded, Settings.base32(plain))
        }
    }
    @Test fun specExampleShapeNormalizesHost() {
        assertEquals("velta://proxy/2/proxy.example.org/1080/N/J5TGM2LDMU", Settings.veltaLink("PROXY.EXAMPLE.ORG", 1080, label = "Office"))
    }
    @Test fun credentialsUseSeparateUppercaseParts() {
        assertEquals("velta://proxy/2/example.org/1080/U/MY/W/MZXQ/N/KBQXG43BM5SQ", Settings.veltaLink("example.org", 1080, "f", "fo"))
    }
    @Test fun noCredentialsOmitsBothPartsAndUnicodeIsUtf8() {
        val link = Settings.veltaLink("example.org", 1080, label = "Привет")
        assertEquals("velta://proxy/2/example.org/1080/N/2CP5DAGQXDILFUFV2GBA", link)
        assertFalse(link.endsWith('/'))
        assertFalse(link.contains('?'))
        assertTrue(link.length < 2048)
    }
    @Test fun rejectsPasswordWithoutUserAndInvalidValues() {
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org", 1080, password = "sample") }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org?x", 1080) }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org", 0) }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org", 1080, label = "line\nbreak") }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org", 1080, user = "x".repeat(129)) }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("example.org", 1080, label = "x".repeat(65)) }
        assertThrows(IllegalArgumentException::class.java) { Settings.veltaLink("x".repeat(2048), 1080) }
    }
}
