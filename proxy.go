package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyConfig holds the proxy configuration.
type ProxyConfig struct {
	ListenAddr string
	Upstream   *url.URL
	BaseFont   []byte
	Selectors  string
}

// Proxy returns an http.Handler that reverse-proxies to the upstream,
// intercepting HTML responses to inject font randomization.
func Proxy(cfg *ProxyConfig) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(cfg.Upstream)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = cfg.Upstream.Host
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			return nil
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		resp.Body.Close()

		seed := time.Now().UnixNano()
		key := GenerateFontKey()

		result, err := RandomizeFont(cfg.BaseFont, seed, printableChars())
		if err != nil {
			log.Printf("font randomization failed: %v", err)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}

		fontCache.Store(key, result.FontBytes)

		fi := newFontInterceptor(cfg.BaseFont)

		var out bytes.Buffer
		if err := RewriteHTML(bytes.NewReader(body), &out, key, result.CharMap, cfg.Selectors, fi); err != nil {
			log.Printf("html rewrite failed: %v", err)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		}

		log.Printf("[%s] proxied %s (font key: %s, %d chars, %d bytes font)",
			resp.Request.URL.Path, time.Now().Format("15:04:05"), key,
			len(result.CharMap.Forward), len(result.FontBytes))

		resp.Body = io.NopCloser(&out)
		resp.Header.Del("Content-Length")
		resp.Header.Del("Content-Encoding")
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_scribble/font/") {
			serveFont(w, r)
			return
		}

		proxy.ServeHTTP(w, r)
	})
}

// fontCache stores generated fonts keyed by their font key.
var fontCache sync.Map

// serveFont handles requests for generated font files.
func serveFont(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	key := strings.TrimPrefix(path, "/_scribble/font/")
	if idx := strings.LastIndex(key, "."); idx != -1 {
		key = key[:idx]
	}

	val, ok := fontCache.Load(key)
	if !ok {
		http.Error(w, "font not found", http.StatusNotFound)
		return
	}

	fontBytes := val.([]byte)
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(fontBytes)
}

// printableChars returns a comprehensive set of printable characters.
func printableChars() []rune {
	chars := make([]rune, 0, 200)
	for r := rune(0x20); r <= 0x7E; r++ {
		chars = append(chars, r)
	}
	for r := rune(0xA0); r <= 0xFF; r++ {
		chars = append(chars, r)
	}
	return chars
}
