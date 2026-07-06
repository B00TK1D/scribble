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
	fi := newFontInterceptor(nil)
	var out strings.Builder
	err := RewriteHTML(strings.NewReader(input), &out, "testkey123", charMap, "*", fi)
	if err != nil {
		t.Fatalf("RewriteHTML: %v", err)
	}

	result := out.String()

	if !strings.Contains(result, "@font-face") {
		t.Error("missing @font-face injection")
	}
	if !strings.Contains(result, "/_scribble/font/testkey123.ttf") {
		t.Error("missing ttf font URL")
	}
	if strings.Contains(result, "Hello World") {
		t.Error("original text 'Hello World' should not appear in output")
	}
	if !strings.Contains(result, `var x = "should not change";`) {
		t.Error("script content should not be modified")
	}
}

func TestAttributeReplacement(t *testing.T) {
	input := `<html><body>
<img alt="Photo of a cat" src="cat.jpg">
<input placeholder="Enter your name">
<button title="Click to submit">OK</button>
<span aria-label="Close dialog">X</span>
</body></html>`

	charMap := NewCharMap([]rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz "), 42)
	fi := newFontInterceptor(nil)
	var out strings.Builder
	RewriteHTML(strings.NewReader(input), &out, "key", charMap, "*", fi)
	result := out.String()

	if strings.Contains(result, "Photo of a cat") {
		t.Error("alt text not replaced")
	}
	if strings.Contains(result, "Enter your name") {
		t.Error("placeholder not replaced")
	}
	if strings.Contains(result, "Click to submit") {
		t.Error("title not replaced")
	}
	if strings.Contains(result, "Close dialog") {
		t.Error("aria-label not replaced")
	}
	if strings.Contains(result, ">OK<") {
		t.Error("button text not replaced")
	}
	if !strings.Contains(result, `src="cat.jpg"`) {
		t.Error("img src should not be modified")
	}
}

func TestCSSContentReplacement(t *testing.T) {
	input := `<html><head>
<style>
.quote::before { content: "Hello World"; }
.single::before { content: 'Test'; }
</style>
</head><body>
<div class="quote">Text</div>
<div style='content: "Inline Style";'>More</div>
</body></html>`

	charMap := NewCharMap([]rune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz "), 42)
	fi := newFontInterceptor(nil)
	var out strings.Builder
	RewriteHTML(strings.NewReader(input), &out, "key", charMap, "*", fi)
	result := out.String()

	if strings.Contains(result, `"Hello World"`) {
		t.Error("CSS content: \"Hello World\" not replaced")
	}
	if strings.Contains(result, `'Test'`) {
		t.Error("CSS content: 'Test' not replaced")
	}
	if strings.Contains(result, `"Inline Style"`) {
		t.Error("inline style content not replaced")
	}
}

func TestSelectors(t *testing.T) {
	input := `<html><head></head><body><p>Test</p></body></html>`
	charMap := NewCharMap([]rune("T"), 42)
	fi := newFontInterceptor(nil)

	var out strings.Builder
	RewriteHTML(strings.NewReader(input), &out, "key", charMap, "", fi)
	result := out.String()
	if !strings.Contains(result, "*{font-family") {
		t.Error("default selector should be *")
	}

	out.Reset()
	RewriteHTML(strings.NewReader(input), &out, "key", charMap, ".protected,.secret", fi)
	result = out.String()
	if !strings.Contains(result, ".protected,.secret{font-family") {
		t.Error("custom selector not applied")
	}
	if strings.Contains(result, "*{font-family") {
		t.Error("should not use * when custom selector provided")
	}
}

func TestFontGeneration(t *testing.T) {
	baseFont, err := readFile("fonts/Roboto-Regular.ttf")
	if err != nil {
		t.Fatalf("read font: %v", err)
	}

	result, err := RandomizeFont(baseFont, 12345, printableChars())
	if err != nil {
		t.Fatalf("RandomizeFont: %v", err)
	}

	if len(result.FontBytes) == 0 {
		t.Fatal("generated font is empty")
	}
	if len(result.CharMap.Forward) == 0 {
		t.Fatal("char map is empty")
	}

	data := result.FontBytes
	sfVersion := binary.BigEndian.Uint32(data[0:4])
	if sfVersion != 0x00010000 {
		t.Errorf("unexpected SFNT version: 0x%08X", sfVersion)
	}

	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	cmapCount := 0
	for i := 0; i < numTables; i++ {
		base := 12 + i*16
		tag := binary.BigEndian.Uint32(data[base : base+4])
		if tag == 0x636D6170 {
			cmapCount++
		}
	}
	if cmapCount != 1 {
		t.Errorf("expected exactly 1 cmap table, got %d", cmapCount)
	}

	t.Logf("Generated TTF: %d bytes, %d tables, %d chars mapped",
		len(data), numTables, len(result.CharMap.Forward))
}

func TestFontIsDifferentEachTime(t *testing.T) {
	baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
	r1, _ := RandomizeFont(baseFont, 100, printableChars())
	r2, _ := RandomizeFont(baseFont, 200, printableChars())

	if len(r1.FontBytes) != len(r2.FontBytes) {
		t.Fatal("fonts should be same size")
	}
	if string(r1.FontBytes) == string(r2.FontBytes) {
		t.Error("fonts with different seeds should not be identical")
	}
}

func TestProxyIntegration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body><p>Sensitive Data Here</p></body></html>`)
	}))
	defer upstream.Close()

	baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
	cfg := &ProxyConfig{
		ListenAddr: ":0",
		Upstream:   mustParseURL(upstream.URL),
		BaseFont:   baseFont,
		Selectors:  ".content",
	}
	proxy := httptest.NewServer(Proxy(cfg))
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/test")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if strings.Contains(html, "Sensitive Data Here") {
		t.Error("original text should not appear")
	}
	if !strings.Contains(html, "@font-face") {
		t.Error("missing @font-face")
	}
	if !strings.Contains(html, ".content{font-family") {
		t.Error("custom selector not applied")
	}

	fontURL := extractFontURL(html)
	fontResp, err := http.Get(proxy.URL + fontURL)
	if err != nil {
		t.Fatalf("font request: %v", err)
	}
	defer fontResp.Body.Close()

	fontBytes, _ := io.ReadAll(fontResp.Body)
	if len(fontBytes) < 100 {
		t.Fatal("font too small")
	}
	t.Logf("Font served: %d bytes", len(fontBytes))
}

func TestPrintableChars(t *testing.T) {
	chars := printableChars()
	seen := make(map[rune]bool)
	for _, c := range chars {
		seen[c] = true
	}
	for r := rune(0x20); r <= 0x7E; r++ {
		if !seen[r] {
			t.Errorf("missing char %q", r)
		}
	}
	t.Logf("printableChars: %d chars", len(chars))
}

func TestSaveFont(t *testing.T) {
	baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
	result, _ := RandomizeFont(baseFont, 42, printableChars())
	os.WriteFile("/tmp/scribble_test.ttf", result.FontBytes, 0644)
	t.Logf("Wrote %d bytes to /tmp/scribble_test.ttf", len(result.FontBytes))
}

func TestFontInterceptor(t *testing.T) {
	// Start a fake font server
	fontServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
		w.Header().Set("Content-Type", "font/ttf")
		w.Write(baseFont)
	}))
	defer fontServer.Close()

	baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
	fi := newFontInterceptor(baseFont)

	// Intercept a font URL
	localPath, err := fi.InterceptURL(fontServer.URL + "/test-font.ttf")
	if err != nil {
		t.Fatalf("InterceptURL: %v", err)
	}
	if !strings.HasPrefix(localPath, "/_scribble/font/") {
		t.Errorf("unexpected local path: %s", localPath)
	}

	// Second call should return cached path
	localPath2, err := fi.InterceptURL(fontServer.URL + "/test-font.ttf")
	if err != nil {
		t.Fatalf("InterceptURL second call: %v", err)
	}
	if localPath != localPath2 {
		t.Errorf("expected cached path: %s != %s", localPath, localPath2)
	}

	// Verify the font was cached
	fontKey := strings.TrimPrefix(localPath, "/_scribble/font/")
	fontKey = strings.TrimSuffix(fontKey, ".ttf")
	val, ok := fontCache.Load(fontKey)
	if !ok {
		t.Fatal("font not found in cache")
	}
	fontBytes := val.([]byte)
	if len(fontBytes) < 100 {
		t.Fatal("cached font too small")
	}
	t.Logf("Intercepted font: %s (%d bytes)", localPath, len(fontBytes))
}

func TestCSSRewrite(t *testing.T) {
	// Start a fake font server
	fontServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
		w.Header().Set("Content-Type", "font/ttf")
		w.Write(baseFont)
	}))
	defer fontServer.Close()

	baseFont, _ := readFile("fonts/Roboto-Regular.ttf")
	fi := newFontInterceptor(baseFont)

	css := fmt.Sprintf(`@font-face {
  font-family: 'Roboto';
  src: url('%s/fonts/Roboto-Regular.ttf') format('truetype');
  font-weight: 400;
}`, fontServer.URL)

	rewritten := fi.RewriteFontFaceCSS(css)

	// The URL should be rewritten to a local path
	if strings.Contains(rewritten, fontServer.URL) {
		t.Error("original font URL not rewritten")
	}
	if !strings.Contains(rewritten, "/_scribble/font/") {
		t.Error("missing local font path in rewritten CSS")
	}
	if !strings.Contains(rewritten, "format('truetype')") {
		t.Error("format string lost in rewrite")
	}

	t.Logf("Rewritten CSS:\n%s", rewritten)
}

func TestGoogleFontsLink(t *testing.T) {
	input := `<html><head>
<link href="https://fonts.googleapis.com/css2?family=Roboto:wght@400&display=swap" rel="stylesheet">
</head><body><p>Hello</p></body></html>`

	charMap := NewCharMap([]rune("Helo"), 42)
	fi := newFontInterceptor(nil)
	var out strings.Builder
	RewriteHTML(strings.NewReader(input), &out, "key", charMap, "*", fi)
	result := out.String()

	// The Google Fonts link should be removed
	if strings.Contains(result, "fonts.googleapis.com") {
		t.Error("Google Fonts link not removed")
	}

	t.Logf("Result (first 500 chars): %s", result[:min(500, len(result))])
}

func extractFontURL(body string) string {
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
