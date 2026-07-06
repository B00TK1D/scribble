# scribble

An HTTP proxy that combats automated web scraping using font randomization.

## How It Works

1. **Font randomization**: On each request, the proxy generates a custom font with a shuffled cmap table — each glyph is assigned to a random Unicode Private Use Area (PUA) codepoint.

2. **Text replacement**: All text content in proxied HTML is replaced with PUA characters — text nodes, attribute values (`title`, `alt`, `placeholder`, `aria-label`), and CSS `content:` strings. On the wire, the HTML contains gibberish. Only when rendered with the custom font does the correct text appear.

3. **CSS injection**: A `@font-face` rule is injected into the HTML `<head>`, applying the randomized font to configurable CSS selectors.

4. **Font serving**: The modified font is served at `/_scribble/font/{key}.ttf`.

A scraper extracting `textContent` or attribute values gets garbage. Only a browser that loads and renders the font can display the real text.

## Usage

```bash
go build -o scribble .

# Proxy example.com on port 8080 (all elements)
./scribble -listen :8080 -upstream https://example.com

# Only protect elements with specific selectors
./scribble -listen :8080 -upstream https://example.com -selectors ".protected,.content"

# Use a custom base font
./scribble -listen :8080 -upstream https://example.com -font /path/to/font.ttf
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-listen` | `:8080` | Address to listen on |
| `-upstream` | (required) | Upstream URL to proxy |
| `-font` | `fonts/Roboto-Regular.ttf` | Path to base TTF font |
| `-selectors` | `*` | CSS selectors to apply the font to |

## Testing

```bash
go test -v ./...
```

## Architecture

```
Client  ──►  scribble (:8080)  ──►  upstream
               │
               ├── Intercepts HTML responses
               ├── Detects Google Fonts <link> tags → removes & rewrites
               ├── Detects @font-face rules → downloads & randomizes external fonts
               ├── Generates randomized font (shuffled cmap)
               ├── Replaces text nodes with PUA characters
               ├── Replaces attribute text (title, alt, placeholder, aria-*)
               ├── Replaces CSS content: "..." strings
               ├── Injects @font-face CSS with randomized fonts
               └── Serves fonts at /_scribble/font/{key}.ttf
```

## Roadmap

| Version | Features |
|---------|----------|
| **v0.1** | Single base font, cmap shuffle, HTML text replacement, CSS injection, basic proxy |
| **v0.2** | Full font with merged cmap (original + PUA), TTF serving |
| **v0.3** | CSS `content:` replacement, HTML attribute replacement, configurable selectors |
| **v0.5** | Google Fonts CDN interception, @font-face interception, proper CSS cascade |
| **v0.6** | Custom font interception: download, remap, and locally serve any fonts referenced in HTML |
