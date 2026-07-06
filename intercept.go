package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// fontInterceptor downloads external fonts, randomizes them, and caches
// the results. Each unique source URL gets its own randomized font.
type fontInterceptor struct {
	client  *http.Client
	baseFont []byte
	mu       sync.Mutex
	cache    map[string]*interceptedFont // originalURL -> result
}

type interceptedFont struct {
	localPath string // e.g. "/_scribble/font/{key}.ttf"
	key       string
}

func newFontInterceptor(baseFont []byte) *fontInterceptor {
	return &fontInterceptor{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseFont: baseFont,
		cache:    make(map[string]*interceptedFont),
	}
}

// InterceptURL downloads a font from the given URL, randomizes it, caches it,
// and returns the local serving path. If the URL was already intercepted,
// returns the cached path.
func (fi *fontInterceptor) InterceptURL(fontURL string) (string, error) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	if cached, ok := fi.cache[fontURL]; ok {
		return cached.localPath, nil
	}

	fontBytes, err := fi.download(fontURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", fontURL, err)
	}

	seed := time.Now().UnixNano()
	key := GenerateFontKey()

	result, err := RandomizeFont(fontBytes, seed, printableChars())
	if err != nil {
		// If randomization fails (e.g., not a valid font), try with base font
		log.Printf("randomize %s failed (%v), using base font", fontURL, err)
		result, err = RandomizeFont(fi.baseFont, seed, printableChars())
		if err != nil {
			return "", fmt.Errorf("randomize: %w", err)
		}
	}

	fontCache.Store(key, result.FontBytes)

	localPath := "/_scribble/font/" + key + ".ttf"
	fi.cache[fontURL] = &interceptedFont{
		localPath: localPath,
		key:       key,
	}

	return localPath, nil
}

func (fi *fontInterceptor) download(fontURL string) ([]byte, error) {
	resp, err := fi.client.Get(fontURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit download to 5MB
	limited := io.LimitReader(resp.Body, 5*1024*1024)
	return io.ReadAll(limited)
}

// fontFaceRe matches @font-face rules in CSS.
var fontFaceRe = regexp.MustCompile(`(?i)@font-face\s*\{[^}]*\}`)

// fontSrcRe matches src: url(...) inside @font-face rules.
var fontSrcRe = regexp.MustCompile(`(?i)src\s*:\s*url\s*\(\s*["']?([^"')]+)["']?\s*\)`)

// InterceptGoogleFonts finds <link> tags pointing to Google Fonts in the HTML,
// downloads the CSS, extracts font URLs, downloads and randomizes each font,
// and returns replacement CSS to inject. The original <link> tags should be
// removed by the caller.
func InterceptGoogleFonts(doc string, fi *fontInterceptor) (string, []string) {
	var cssParts []string
	var linkPatterns []string

	matches := googleFontsLinkRe.FindAllStringSubmatch(doc, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		cssURL := m[1]
		linkPatterns = append(linkPatterns, m[0])

		css, err := fi.downloadCSS(cssURL)
		if err != nil {
			log.Printf("download google fonts CSS from %s: %v", cssURL, err)
			continue
		}

		rewrittenCSS := fi.rewriteFontFaceCSS(css)
		if rewrittenCSS != "" {
			cssParts = append(cssParts, rewrittenCSS)
		}
	}

	return strings.Join(cssParts, "\n"), linkPatterns
}

// RewriteFontFaceCSS rewrites @font-face rules in CSS, replacing external
// font URLs with local randomized versions.
func (fi *fontInterceptor) RewriteFontFaceCSS(css string) string {
	return fi.rewriteFontFaceCSS(css)
}

func (fi *fontInterceptor) downloadCSS(cssURL string) (string, error) {
	resp, err := fi.client.Get(cssURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (fi *fontInterceptor) rewriteFontFaceCSS(css string) string {
	return fontFaceRe.ReplaceAllStringFunc(css, func(block string) string {
		return fontSrcRe.ReplaceAllStringFunc(block, func(match string) string {
			parts := fontSrcRe.FindStringSubmatch(match)
			if len(parts) < 2 {
				return match
			}
			fontURL := parts[1]

			// Resolve relative URLs
			resolvedURL := fontURL
			if !strings.HasPrefix(fontURL, "http://") && !strings.HasPrefix(fontURL, "https://") {
				// Relative URL — can't resolve without a base URL
				return match
			}

			localPath, err := fi.InterceptURL(resolvedURL)
			if err != nil {
				log.Printf("intercept font %s: %v", fontURL, err)
				return match
			}

			// Rewrite the URL to point to our local copy
			return strings.Replace(match, fontURL, localPath, 1)
		})
	})
}

// InterceptFontFaceInHTML finds @font-face rules in <style> blocks and inline
// style attributes, downloads external font sources, and rewrites them.
// Returns the number of fonts intercepted.
func InterceptFontFaceInHTML(r io.Reader, w io.Writer, fi *fontInterceptor) (int, error) {
	doc, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}

	html := string(doc)
	count := 0

	// Find @font-face rules in <style> blocks
	html = fontFaceRe.ReplaceAllStringFunc(html, func(block string) string {
		rewritten := fontSrcRe.ReplaceAllStringFunc(block, func(match string) string {
			parts := fontSrcRe.FindStringSubmatch(match)
			if len(parts) < 2 {
				return match
			}
			fontURL := parts[1]
			if !strings.HasPrefix(fontURL, "http://") && !strings.HasPrefix(fontURL, "https://") {
				return match
			}
			localPath, err := fi.InterceptURL(fontURL)
			if err != nil {
				log.Printf("intercept font %s: %v", fontURL, err)
				return match
			}
			count++
			return strings.Replace(match, fontURL, localPath, 1)
		})
		return rewritten
	})

	_, err = w.Write([]byte(html))
	return count, err
}

// inlineStyleFontURLRe matches font URLs in inline style attributes.
var inlineStyleFontURLRe = regexp.MustCompile(`(?i)url\s*\(\s*["']?(https?://[^"')]+\.ttf[^"')]*|https?://[^"')]+\.woff2?[^"')]*|https?://[^"')]+\.otf[^"')]*)["']?\s*\)`)

// InterceptInlineStyleFonts rewrites font URLs in inline style attributes.
func (fi *fontInterceptor) RewriteInlineStyleFontURLs(style string) string {
	return inlineStyleFontURLRe.ReplaceAllStringFunc(style, func(match string) string {
		parts := inlineStyleFontURLRe.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		fontURL := parts[1]
		localPath, err := fi.InterceptURL(fontURL)
		if err != nil {
			return match
		}
		return strings.Replace(match, fontURL, localPath, 1)
	})
}
