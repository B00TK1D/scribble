package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestRewriteHTML(t *testing.T) {
	input := `<!DOCTYPE html>
<html>
<head><title>Test</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test page.</p>
<script>var x = "should not change";</script>
</body>
</html>`

	charMap := NewCharMap([]rune("ABCDEFGHILMNOPRSTWabcdefghilmnoprstw "), 42)
	var out strings.Builder
	err := RewriteHTML(strings.NewReader(input), &out, "testkey123", charMap)
	if err != nil {
		t.Fatalf("RewriteHTML: %v", err)
	}

	result := out.String()

	// Verify @font-face is injected
	if !strings.Contains(result, "@font-face") {
		t.Error("missing @font-face injection")
	}
	if !strings.Contains(result, "/_scribble/font/testkey123.ttf") {
		t.Error("missing font URL")
	}

	// Verify text is replaced with PUA characters
	if strings.Contains(result, "Hello World") {
		t.Error("original text 'Hello World' should not appear in output")
	}

	// Verify script content is NOT modified
	if !strings.Contains(result, `var x = "should not change";`) {
		t.Error("script content should not be modified")
	}

	fmt.Println("=== Rewritten HTML ===")
	fmt.Println(result)
}

func TestFontGeneration(t *testing.T) {
	baseFont, err := readFile("fonts/Roboto-Regular.ttf")
	if err != nil {
		t.Fatalf("read font: %v", err)
	}

	result, err := RandomizeFont(baseFont, 12345)
	if err != nil {
		t.Fatalf("RandomizeFont: %v", err)
	}

	if len(result.FontBytes) == 0 {
		t.Error("generated font is empty")
	}

	if len(result.CharMap.Forward) == 0 {
		t.Error("char map is empty")
	}

	// Verify the generated font is valid (starts with OTF header)
	if len(result.FontBytes) >= 4 {
		tag := string(result.FontBytes[:4])
		if tag != "\x00\x01\x00\x00" && tag != "OTTO" {
			t.Errorf("unexpected font header: %q", tag)
		}
	}

	// Verify table directory is ordered and head/cmap are valid
	data := result.FontBytes
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	var prevTag uint32
	var headOK, cmapOK bool
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(data[base : base+4])
		offset := binary.BigEndian.Uint32(data[base+8 : base+12])
		length := binary.BigEndian.Uint32(data[base+12 : base+16])

		if tag < prevTag {
			t.Errorf("table %d: directory not ordered (tag 0x%08X < 0x%08X)", i, tag, prevTag)
		}
		prevTag = tag

		if offset+length > uint32(len(data)) {
			tagStr := string([]byte{byte(tag >> 24), byte(tag >> 16), byte(tag >> 8), byte(tag)})
			t.Errorf("table %s: extends beyond file (offset=%d length=%d fileSize=%d)",
				tagStr, offset, length, len(data))
		}

		if tag == 0x68656164 { // "head"
			major := binary.BigEndian.Uint16(data[offset : offset+2])
			if major != 1 {
				t.Errorf("head majorVersion: got %d, want 1", major)
			}
			chkAdj := binary.BigEndian.Uint32(data[offset+8 : offset+12])
			if chkAdj != 0 {
				t.Errorf("head checksumAdjustment: got 0x%08X, want 0", chkAdj)
			}
			headOK = true
		}

		if tag == 0x636D6170 { // "cmap"
			if offset+4 <= uint32(len(data)) {
				cmapVer := binary.BigEndian.Uint16(data[offset : offset+2])
				cmapNum := binary.BigEndian.Uint16(data[offset+2 : offset+4])
				t.Logf("cmap: version=%d numTables=%d offset=%d length=%d", cmapVer, cmapNum, offset, length)
			}
			cmapOK = true
		}
	}
	if !headOK {
		t.Error("head table not found in output")
	}
	if !cmapOK {
		t.Error("cmap table not found in output")
	}

	// Verify we can re-parse the generated font's cmap
	reparsed := parseCmap(result.FontBytes[cmapOffset(result.FontBytes):])
	if len(reparsed) == 0 {
		t.Error("re-parsed cmap has no entries")
	}

	// Verify PUA codepoints are in the re-parsed cmap
	puaCount := 0
	for cp := range reparsed {
		if cp >= 0xE000 && cp <= 0xF8FF {
			puaCount++
		}
	}
	if puaCount == 0 {
		t.Error("re-parsed cmap has no PUA entries")
	}

	t.Logf("Generated font: %d bytes, %d tables, %d chars mapped, %d PUA entries in re-parsed cmap",
		len(result.FontBytes), numTables, len(result.CharMap.Forward), puaCount)
}

// cmapOffset finds the cmap table offset in a font binary.
func cmapOffset(data []byte) uint32 {
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(data[base : base+4])
		if tag == 0x636D6170 { // "cmap"
			return binary.BigEndian.Uint32(data[base+8 : base+12])
		}
	}
	return 0
}

func TestProxyIntegration(t *testing.T) {
	// Start a test upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body><p>Sensitive Data Here</p></body></html>`)
	}))
	defer upstream.Close()

	// Read base font
	baseFont, err := readFile("fonts/Roboto-Regular.ttf")
	if err != nil {
		t.Fatalf("read font: %v", err)
	}

	// Create proxy
	cfg := &ProxyConfig{
		ListenAddr: ":0",
		Upstream:   mustParseURL(upstream.URL),
		BaseFont:   baseFont,
	}
	proxy := httptest.NewServer(Proxy(cfg))
	defer proxy.Close()

	// Request through proxy
	resp, err := http.Get(proxy.URL + "/test")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)

	if strings.Contains(body, "Sensitive Data Here") {
		t.Error("original text should not appear in proxied response")
	}

	if !strings.Contains(body, "@font-face") {
		t.Error("missing @font-face injection")
	}

	if !strings.Contains(body, "scribble") {
		t.Error("missing scribble font-family")
	}

	// Extract font URL and request it
	fontURL := extractFontURL(body)
	if fontURL == "" {
		t.Fatal("could not extract font URL from response")
	}

	fontResp, err := http.Get(proxy.URL + fontURL)
	if err != nil {
		t.Fatalf("font request: %v", err)
	}
	defer fontResp.Body.Close()

	if fontResp.StatusCode != 200 {
		t.Fatalf("font request status: %d", fontResp.StatusCode)
	}

	fontBytes, err := io.ReadAll(fontResp.Body)
	if err != nil {
		t.Fatalf("read font: %v", err)
	}

	// Verify font header (TrueType starts with 0x00010000)
	if len(fontBytes) < 4 {
		t.Fatal("font too small")
	}
	tag := string(fontBytes[:4])
	if tag != "\x00\x01\x00\x00" && tag != "OTTO" {
		t.Errorf("unexpected font header: %q", tag)
	}

	t.Logf("Font served: %d bytes", len(fontBytes))
	t.Logf("Proxied response (first 500 chars): %s", body[:min(500, len(body))])
}

func extractFontURL(body string) string {
	// Look for src:url('...')
	start := strings.Index(body, "src:url('")
	if start == -1 {
		return ""
	}
	start += len("src:url('")
	end := strings.Index(body[start:], "'")
	if end == -1 {
		return ""
	}
	return body[start : start+end]
}

func TestSaveFont(t *testing.T) {
	baseFont, err := readFile("fonts/Roboto-Regular.ttf")
	if err != nil {
		t.Fatalf("read font: %v", err)
	}
	result, err := RandomizeFont(baseFont, 42)
	if err != nil {
		t.Fatalf("RandomizeFont: %v", err)
	}
	os.WriteFile("/tmp/scribble_test.ttf", result.FontBytes, 0644)
	t.Logf("Wrote %d bytes to /tmp/scribble_test.ttf", len(result.FontBytes))
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}


