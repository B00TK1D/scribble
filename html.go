package main

import (
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// RewriteHTML parses the HTML from r, replaces all text content using the
// character map, injects the @font-face CSS, and writes the result to w.
func RewriteHTML(r io.Reader, w io.Writer, fontKey string, charMap *CharMap) error {
	doc, err := html.Parse(r)
	if err != nil {
		return fmt.Errorf("parse html: %w", err)
	}

	// Walk and replace text nodes
	walkTextNodes(doc, charMap)

	// Inject style into <head>
	injectStyle(doc, fontKey)

	return html.Render(w, doc)
}

// walkTextNodes recursively walks the DOM and replaces text in text nodes
// using the character map. Skips <script>, <style>, and <noscript> elements.
func walkTextNodes(n *html.Node, charMap *CharMap) {
	if n.Type == html.TextNode {
		// Don't modify text inside script, style, or noscript tags
		if !isProtectedElement(n.Parent) {
			n.Data = replaceText(n.Data, charMap)
		}
		return
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkTextNodes(c, charMap)
	}
}

// isProtectedElement returns true if the node is a <script>, <style>,
// or <noscript> element whose text content should not be modified.
func isProtectedElement(n *html.Node) bool {
	if n == nil {
		return false
	}
	if n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript:
		return true
	}
	return false
}

// replaceText replaces each character in s with its PUA equivalent
// from the character map. Characters not in the map are left unchanged.
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

// injectStyle finds the <head> element and prepends a <style> tag that
// loads the scribble font and applies it to all elements.
func injectStyle(doc *html.Node, fontKey string) {
	head := findElement(doc, atom.Head)
	if head == nil {
		// No <head> found, create one
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

	css := fmt.Sprintf(`@font-face{font-family:'scribble';src:url('/_scribble/font/%s.ttf') format('truetype');font-display:block}*{font-family:'scribble',sans-serif!important}`, fontKey)

	styleNode := &html.Node{
		Type: html.TextNode,
		Data: css,
	}
	styleEl := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Style,
		Data:     "style",
	}
	styleEl.AppendChild(styleNode)

	// Prepend to <head>
	if head.FirstChild != nil {
		head.InsertBefore(styleEl, head.FirstChild)
	} else {
		head.AppendChild(styleEl)
	}
}

// findElement performs a depth-first search for the first element with
// the given atom tag name.
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
