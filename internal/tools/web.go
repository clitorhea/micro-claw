package tools

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hrefRegex = regexp.MustCompile(`(?i)href=["']([^"']+)["']`)

// WebScrape fetches a URL and strips HTML tags to return clean plain text.
func WebScrape(targetURL string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(targetURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http error: status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return CleanHTML(string(bodyBytes)), nil
}

// WebCrawl crawls a URL, extracts all hyperlinks, and filters those belonging to the same host domain.
func WebCrawl(targetURL string) ([]string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	parsedTarget, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	resp, err := client.Get(targetURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	matches := hrefRegex.FindAllStringSubmatch(string(bodyBytes), -1)
	seen := make(map[string]bool)
	var links []string

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		rawLink := match[1]

		// Resolve relative URLs
		u, err := url.Parse(rawLink)
		if err != nil {
			continue
		}
		resolved := parsedTarget.ResolveReference(u)

		// Filter to match domain only
		if resolved.Host == parsedTarget.Host {
			cleaned := resolved.String()
			if !seen[cleaned] {
				seen[cleaned] = true
				links = append(links, cleaned)
			}
		}
	}

	return links, nil
}

// CleanHTML strips HTML tags, script/style blocks, and trims excessive whitespaces.
func CleanHTML(html string) string {
	// Strip script blocks
	reScript := regexp.MustCompile(`(?s)<script.*?>.*?</script>`)
	html = reScript.ReplaceAllString(html, " ")

	// Strip style blocks
	reStyle := regexp.MustCompile(`(?s)<style.*?>.*?</style>`)
	html = reStyle.ReplaceAllString(html, " ")

	// Strip HTML tags using standard state machine
	var builder strings.Builder
	inTag := false
	for i := 0; i < len(html); i++ {
		char := html[i]
		if char == '<' {
			inTag = true
			continue
		}
		if char == '>' {
			inTag = false
			continue
		}
		if !inTag {
			builder.WriteByte(char)
		}
	}

	// Unify spacing and newlines
	lines := strings.Split(builder.String(), "\n")
	var cleanedLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cleanedLines = append(cleanedLines, trimmed)
		}
	}

	return strings.Join(cleanedLines, "\n")
}

// SearchDuckDuckGo searches the web using DuckDuckGo's HTML search interface.
func SearchDuckDuckGo(query string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	reqUrl := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", reqUrl, nil)
	if err != nil {
		return "", err
	}

	// Set standard User-Agent to prevent bot detection blocks
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status code %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	html := string(bodyBytes)

	reTitle := regexp.MustCompile(`<a class="result__title"[^>]*href="([^"]+)"[^>]*>(?s)(.*?)</a>`)
	reSnippet := regexp.MustCompile(`<a class="result__snippet"[^>]*>(?s)(.*?)</a>`)

	titles := reTitle.FindAllStringSubmatch(html, -1)
	snippets := reSnippet.FindAllStringSubmatch(html, -1)

	var results []string
	limit := len(titles)
	if len(snippets) < limit {
		limit = len(snippets)
	}
	if limit > 6 {
		limit = 6 // Return top 6 search results
	}

	for i := 0; i < limit; i++ {
		link := titles[i][1]

		// Resolve DuckDuckGo query redirection parameter
		if strings.Contains(link, "uddg=") {
			u, err := url.Parse(link)
			if err == nil {
				if uddg := u.Query().Get("uddg"); uddg != "" {
					link = uddg
				}
			}
		} else if strings.HasPrefix(link, "//") {
			link = "https:" + link
		}

		titleText := CleanHTML(titles[i][2])
		snippetText := CleanHTML(snippets[i][1])

		results = append(results, fmt.Sprintf("[%d] %s\nURL: %s\nSnippet: %s\n", i+1, titleText, link, snippetText))
	}

	if len(results) == 0 {
		return "No search results found.", nil
	}

	return strings.Join(results, "\n---\n"), nil
}
