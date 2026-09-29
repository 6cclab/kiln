package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/andrepato/harness/internal/tool"
)

// Limits for web_fetch. A page is read up to webFetchMaxBytes, reduced to
// text, and returned webFetchChunk characters at a time: most pages fit in
// one chunk, and a long one is paged with offset rather than dropped into
// the context whole.
const (
	webFetchTimeout  = 30 * time.Second
	webFetchMaxBytes = 5 << 20
	webFetchChunk    = 20000
)

var webFetchParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"url": {"type": "string", "description": "The http(s) URL to fetch"},
		"offset": {"type": "number", "description": "Character offset to continue a long page from (from a previous result's footer)"}
	},
	"required": ["url"]
}`)

// WebFetchTool builds the web_fetch built-in: fetch a URL and return its
// readable text. HTML is reduced to text with headings, list items, links
// and code blocks kept; scripts, styles and page chrome are dropped. Other
// text types (JSON, plain text, markdown) come back as they are.
func WebFetchTool(client *http.Client) *tool.Tool {
	if client == nil {
		client = &http.Client{Timeout: webFetchTimeout}
	}
	return &tool.Tool{
		Name:  "web_fetch",
		Label: "web_fetch",
		Description: "Fetch a web page or other http(s) URL and return its readable text (HTML reduced to text with " +
			"headings, lists, links and code blocks kept). Use it to read documentation, release notes, issues or API " +
			"references. Long pages are returned in parts: pass the offset from the result's footer to read on.",
		Parameters: webFetchParameters,
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in struct {
				URL    string  `json:"url"`
				Offset float64 `json:"offset"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			text, final, err := fetchReadable(ctx, client, in.URL)
			if err != nil {
				return tool.Errorf("%s", err), nil
			}
			return tool.Text(pageChunk(text, final, int(in.Offset))), nil
		},
	}
}

// fetchReadable fetches rawURL and returns its readable text and the URL
// it finally came from (after redirects).
func fetchReadable(ctx context.Context, client *http.Client, rawURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", fmt.Errorf("not an http(s) URL: %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "kiln (terminal coding agent; web_fetch)")
	req.Header.Set("Accept", "text/html, text/markdown, text/plain, application/json;q=0.9, */*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetching %s: %w", u, err)
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", final, err)
	}
	truncated := len(body) > webFetchMaxBytes
	if truncated {
		body = body[:webFetchMaxBytes]
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("%s returned %s", final, resp.Status)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var text string
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml" || (mediaType == "" && looksLikeHTML(body)):
		text, err = htmlToText(string(body), resp.Request.URL)
		if err != nil {
			return "", "", fmt.Errorf("reading %s as HTML: %w", final, err)
		}
	case strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "json") || strings.HasSuffix(mediaType, "xml") || mediaType == "":
		text = string(body)
	default:
		return "", "", fmt.Errorf("%s is %s, not text; web_fetch reads pages, not binary files", final, mediaType)
	}
	if truncated {
		text += "\n\n[page cut at 5 MB]"
	}
	return text, final, nil
}

func looksLikeHTML(b []byte) bool {
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

// pageChunk returns the part of text starting at offset, with a header
// naming the source and, when more remains, a footer with the next offset.
func pageChunk(text, source string, offset int) string {
	runes := []rune(text)
	if offset < 0 || offset >= len(runes) {
		if offset == 0 {
			return fmt.Sprintf("Source: %s\n\n(empty page)", source)
		}
		return fmt.Sprintf("Source: %s\n\n(offset %d is past the end: the page is %d characters)", source, offset, len(runes))
	}
	end := min(offset+webFetchChunk, len(runes))
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s\n\n", source)
	b.WriteString(string(runes[offset:end]))
	if end < len(runes) {
		fmt.Fprintf(&b, "\n\n[characters %d-%d of %d; call web_fetch again with offset %d to read on]", offset, end, len(runes), end)
	}
	return b.String()
}

// skipped elements carry no readable content.
var skipped = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true, "canvas": true,
	"iframe": true, "nav": true, "footer": true, "form": true, "button": true, "select": true, "head": true,
}

// blockLevel elements start on a new line.
var blockLevel = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true, "header": true, "aside": true,
	"ul": true, "ol": true, "table": true, "tr": true, "blockquote": true, "dl": true, "dt": true, "dd": true,
	"figure": true, "figcaption": true, "details": true, "summary": true, "hr": true,
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// htmlToText reduces an HTML document to readable text: headings as
// "#"-prefixed lines, list items as "- ", links as "text (url)" with the
// URL made absolute, pre blocks fenced, and the page title first.
func htmlToText(doc string, base *url.URL) (string, error) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if t := findTitle(root); t != "" {
		b.WriteString("# " + t + "\n\n")
	}
	body := findElement(root, "body")
	if body == nil {
		body = root
	}
	if m := findElement(body, "main"); m != nil {
		body = m // most docs sites wrap the content in <main>
	}
	var walk func(n *html.Node, inPre bool)
	newline := func() {
		s := b.String()
		if len(s) > 0 && !strings.HasSuffix(s, "\n") {
			b.WriteString("\n")
		}
	}
	walk = func(n *html.Node, inPre bool) {
		switch n.Type {
		case html.TextNode:
			if inPre {
				b.WriteString(n.Data)
				return
			}
			text := strings.Join(strings.Fields(n.Data), " ")
			if text == "" {
				return
			}
			s := b.String()
			if len(s) > 0 && !strings.HasSuffix(s, "\n") && !strings.HasSuffix(s, " ") &&
				(n.Data[0] == ' ' || n.Data[0] == '\n' || n.Data[0] == '\t') {
				b.WriteString(" ")
			}
			b.WriteString(text)
			if last := n.Data[len(n.Data)-1]; last == ' ' || last == '\n' || last == '\t' {
				b.WriteString(" ")
			}
			return
		case html.ElementNode:
			if skipped[n.Data] {
				return
			}
			switch n.Data {
			case "br":
				b.WriteString("\n")
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				newline()
				b.WriteString("\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, false)
				}
				b.WriteString("\n\n")
				return
			case "li":
				newline()
				b.WriteString("- ")
			case "pre":
				newline()
				b.WriteString("\n```\n")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, true)
				}
				newline()
				b.WriteString("```\n\n")
				return
			case "code":
				if !inPre {
					b.WriteString("`")
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						walk(c, false)
					}
					b.WriteString("`")
					return
				}
			case "a":
				href := attr(n, "href")
				start := b.Len()
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, inPre)
				}
				label := strings.TrimSpace(b.String()[start:])
				if href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") && label != "" {
					if ref, err := base.Parse(href); err == nil {
						b.WriteString(" (" + ref.String() + ")")
					}
				}
				return
			case "td", "th":
				b.WriteString(" | ")
			}
			if blockLevel[n.Data] {
				newline()
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inPre)
		}
		if n.Type == html.ElementNode && (blockLevel[n.Data] || n.Data == "li") {
			newline()
			if n.Data == "p" || n.Data == "blockquote" || n.Data == "table" {
				b.WriteString("\n")
			}
		}
	}
	walk(body, false)
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	out := blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(out), nil
}

func findTitle(n *html.Node) string {
	if t := findElement(n, "title"); t != nil && t.FirstChild != nil {
		return strings.Join(strings.Fields(t.FirstChild.Data), " ")
	}
	return ""
}

func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, name); f != nil {
			return f
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
