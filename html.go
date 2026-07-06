package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// userVisibleAttributes are HTML attributes whose values are displayed to users.
var userVisibleAttributes = map[string]bool{
	"title":            true,
	"alt":              true,
	"placeholder":      true,
	"aria-label":       true,
	"aria-description": true,
	"value":            true,
}

// RewriteHTML parses the HTML from r, replaces all text content using the
// character map, intercepts external fonts, injects CSS, and writes to w.
func RewriteHTML(r io.Reader, w io.Writer, fontKey string, charMap *CharMap, selectors string, fi *fontInterceptor) error {
	doc, err := html.Parse(r)
	if err != nil {
		return fmt.Errorf("parse html: %w", err)
	}

	// Intercept Google Fonts <link> tags
	googleCSS, linkNodes := collectGoogleFontLinks(doc)
	for _, node := range linkNodes {
		node.Parent.RemoveChild(node)
	}

	// Intercept @font-face in <style> blocks
	hasExternalFonts := false
	if googleCSS != "" {
		hasExternalFonts = true
	}
	interceptStyleBlocks(doc, fi, &hasExternalFonts)

	// Rewrite inline style attributes that reference font URLs
	interceptInlineStyles(doc, fi, &hasExternalFonts)

	// Replace text content
	walkTextNodes(doc, charMap)
	walkAttributes(doc, charMap)
	walkCSSContent(doc, charMap)

	// Inject styles
	if hasExternalFonts {
		// External fonts found: inject rewritten Google Fonts CSS + base font fallback
		injectExternalFontCSS(doc, fontKey, googleCSS, selectors)
	} else {
		// No external fonts: use base font with configured selectors
		injectBaseFontCSS(doc, fontKey, selectors)
	}

	return html.Render(w, doc)
}

// walkTextNodes recursively walks the DOM and replaces text in text nodes.
func walkTextNodes(n *html.Node, charMap *CharMap) {
	if n.Type == html.TextNode {
		if !isProtectedElement(n.Parent) {
			n.Data = replaceText(n.Data, charMap)
		}
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkTextNodes(c, charMap)
	}
}

// walkAttributes replaces text in user-visible HTML attributes.
func walkAttributes(n *html.Node, charMap *CharMap) {
	if n.Type == html.ElementNode {
		for i := range n.Attr {
			if userVisibleAttributes[n.Attr[i].Key] {
				n.Attr[i].Val = replaceText(n.Attr[i].Val, charMap)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkAttributes(c, charMap)
	}
}

var cssContentRe = regexp.MustCompile(`(content\s*:\s*)(["'])([^"']*)(["'])`)

func walkCSSContent(n *html.Node, charMap *CharMap) {
	if n.Type == html.ElementNode {
		if n.DataAtom == atom.Style {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					c.Data = replaceCSSContentStrings(c.Data, charMap)
				}
			}
		}
		for i := range n.Attr {
			if n.Attr[i].Key == "style" {
				n.Attr[i].Val = replaceCSSContentStrings(n.Attr[i].Val, charMap)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkCSSContent(c, charMap)
	}
}

func replaceCSSContentStrings(css string, charMap *CharMap) string {
	return cssContentRe.ReplaceAllStringFunc(css, func(match string) string {
		parts := cssContentRe.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		return parts[1] + parts[2] + replaceText(parts[3], charMap) + parts[4]
	})
}

func isProtectedElement(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript:
		return true
	}
	return false
}

func replaceText(s string, charMap *CharMap) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if mapped, ok := charMap.Forward[r]; ok {
			b.WriteRune(mapped)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// googleFontsLinkRe matches <link> tags pointing to Google Fonts.
var googleFontsLinkRe = regexp.MustCompile(`fonts\.googleapis\.com`)

// collectGoogleFontLinks finds and removes <link> tags pointing to Google Fonts.
// Returns the combined CSS URL content and the link nodes for removal.
func collectGoogleFontLinks(doc *html.Node) (string, []*html.Node) {
	var cssURLs []string
	var linkNodes []*html.Node

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Link {
			href := getAttr(n, "href")
			if href != "" && googleFontsLinkRe.MatchString(href) {
				cssURLs = append(cssURLs, href)
				linkNodes = append(linkNodes, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(cssURLs) == 0 {
		return "", nil
	}

	// Download CSS from all Google Fonts URLs
	fi := newFontInterceptor(nil) // temporary, just for downloading
	var cssParts []string
	for _, cssURL := range cssURLs {
		css, err := fi.downloadCSS(cssURL)
		if err != nil {
			continue
		}
		cssParts = append(cssParts, css)
	}

	return strings.Join(cssParts, "\n"), linkNodes
}

// interceptStyleBlocks finds @font-face rules in <style> elements,
// downloads external font sources, and rewrites the URLs.
func interceptStyleBlocks(doc *html.Node, fi *fontInterceptor, hasExternal *bool) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Style {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					rewritten := fi.RewriteFontFaceCSS(c.Data)
					if rewritten != c.Data {
						*hasExternal = true
						c.Data = rewritten
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// interceptInlineStyles rewrites font URLs in inline style attributes.
func interceptInlineStyles(doc *html.Node, fi *fontInterceptor, hasExternal *bool) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i := range n.Attr {
				if n.Attr[i].Key == "style" {
					rewritten := fi.RewriteInlineStyleFontURLs(n.Attr[i].Val)
					if rewritten != n.Attr[i].Val {
						*hasExternal = true
						n.Attr[i].Val = rewritten
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// injectExternalFontCSS injects CSS for intercepted external fonts plus
// a base font fallback for the randomized text.
func injectExternalFontCSS(doc *html.Node, fontKey string, googleCSS string, selectors string) {
	head := findOrCreateHead(doc)

	var css strings.Builder

	// Base font for PUA-replaced text
	css.WriteString(fmt.Sprintf(
		"@font-face{font-family:'scribble';src:url('/_scribble/font/%s.ttf') format('truetype');font-display:block}",
		fontKey,
	))

	// Rewritten Google Fonts CSS (already has local URLs)
	if googleCSS != "" {
		css.WriteString("\n")
		css.WriteString(googleCSS)
	}

	// Apply scribble font to configured selectors for PUA text
	if selectors == "" {
		selectors = "*"
	}
	css.WriteString(fmt.Sprintf(
		"\n%s{font-family:'%s',%s}",
		selectors, "scribble", "inherit",
	))

	injectCSSElement(head, css.String())
}

// injectBaseFontCSS injects the base font CSS (no external fonts found).
func injectBaseFontCSS(doc *html.Node, fontKey string, selectors string) {
	head := findOrCreateHead(doc)
	if selectors == "" {
		selectors = "*"
	}
	css := fmt.Sprintf(
		"@font-face{font-family:'scribble';src:url('/_scribble/font/%s.ttf') format('truetype');font-display:block}%s{font-family:'scribble',sans-serif!important}",
		fontKey, selectors,
	)
	injectCSSElement(head, css)
}

func findOrCreateHead(doc *html.Node) *html.Node {
	head := findElement(doc, atom.Head)
	if head == nil {
		head = &html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Head,
			Data:     "head",
		}
		if doc.FirstChild != nil {
			doc.InsertBefore(head, doc.FirstChild)
		} else {
			doc.AppendChild(head)
		}
	}
	return head
}

func injectCSSElement(head *html.Node, css string) {
	styleEl := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Style,
		Data:     "style",
	}
	styleEl.AppendChild(&html.Node{
		Type: html.TextNode,
		Data: css,
	})

	if head.FirstChild != nil {
		head.InsertBefore(styleEl, head.FirstChild)
	} else {
		head.AppendChild(styleEl)
	}
}

func findElement(n *html.Node, tag atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
