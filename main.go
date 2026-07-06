package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
)

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	upstream := flag.String("upstream", "", "upstream URL to proxy (required)")
	fontPath := flag.String("font", "", "path to base TTF font (default: fonts/Roboto-Regular.ttf)")
	selectors := flag.String("selectors", "*", "CSS selectors to apply the font to (default: all elements)")
	flag.Parse()

	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "error: -upstream is required")
		flag.Usage()
		os.Exit(1)
	}

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("invalid upstream URL: %v", err)
	}

	// Load base font
	var baseFont []byte
	if *fontPath != "" {
		baseFont, err = os.ReadFile(*fontPath)
		if err != nil {
			log.Fatalf("read font: %v", err)
		}
	} else {
		baseFont, err = os.ReadFile("fonts/Roboto-Regular.ttf")
		if err != nil {
			log.Fatalf("read default font: %v (use -font flag or place Roboto-Regular.ttf in fonts/)", err)
		}
	}

	log.Printf("scribble proxy listening on %s -> %s", *listen, upstreamURL)

	cfg := &ProxyConfig{
		ListenAddr: *listen,
		Upstream:   upstreamURL,
		BaseFont:   baseFont,
		Selectors:  *selectors,
	}

	if err := http.ListenAndServe(*listen, Proxy(cfg)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
