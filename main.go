package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
)

// Top 10 RSS feeds for software engineers
var RSS_FEEDS = []string{
	"https://habr.com/ru/rss/articles/",
	"https://hnrss.org/frontpage",               // Hacker News
	"https://dev.to/feed",                       // Dev.to
	"https://github.blog/feed/",                 // GitHub Blog
	"https://stackoverflow.blog/feed/",          // Stack Overflow Blog
	"https://martinfowler.com/feed.atom",        // Martin Fowler
	"https://blog.golang.org/feed.atom",         // Go Blog
	"https://aws.amazon.com/blogs/aws/feed/",    // AWS News
	"https://www.reddit.com/r/programming/.rss", // r/programming
	"https://thenewstack.io/feed/",              // The New Stack
}

const AI_PROMPT = `Summarize this article in plain text with simple formatting.

Format rules:
- Use **bold** for section headers
- Use bullet points (•) for lists
- Keep it clean and readable
- NO HTML tags

Structure:
**Summary:** 2-3 sentences

**Key Points:**
- Point 1
- Point 2
- Point 3

**My Thoughts:** Your analysis

**Rating:** X/10 - Brief explanation

If you can't summarize, output: AI FAILED

Title: %s

Content:
%s`
const STATE_FILE = "state.json"
const MAX_POSTS_PER_RUN = 1

type RSS struct {
	Channel struct {
		Items []Item `xml:"item"`
	} `xml:"channel"`
}

type Item struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"` // Some RSS feeds include short description
}

// convertToTelegramHTML converts simple markdown to Telegram-compatible HTML
func convertToTelegramHTML(text string) string {
	// Convert **bold** to <b>bold</b>
	re := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	text = re.ReplaceAllString(text, "<b>$1</b>")

	// Convert *italic* to <i>italic</i>
	re = regexp.MustCompile(`\*([^*]+)\*`)
	text = re.ReplaceAllString(text, "<i>$1</i>")

	// Escape special HTML characters
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")

	// Restore our converted tags
	text = strings.ReplaceAll(text, "&lt;b&gt;", "<b>")
	text = strings.ReplaceAll(text, "&lt;/b&gt;", "</b>")
	text = strings.ReplaceAll(text, "&lt;i&gt;", "<i>")
	text = strings.ReplaceAll(text, "&lt;/i&gt;", "</i>")

	return text
}

func loadState() map[string]bool {
	state := map[string]bool{}
	data, err := os.ReadFile(STATE_FILE)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(data, &state)
	return state
}

func saveState(state map[string]bool) {
	data, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(STATE_FILE, data, 0644)
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sendToTelegram(token, chatID, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	body := map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s", string(rb))
	}
	return nil
}

func fetchRSS(url string) (*RSS, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}

	var rss RSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	return &rss, nil
}

// fetchArticleContent extracts text content from a URL
func fetchArticleContent(url string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parse failed: %w", err)
	}

	// Remove script, style, nav, footer, header elements
	doc.Find("script, style, nav, footer, header, aside, .advertisement, .ad").Remove()

	// Try to find main content (common article selectors)
	var text string
	selectors := []string{
		"article",
		"[role='main']",
		".post-content",
		".article-content",
		".entry-content",
		".content",
		"main",
	}

	for _, selector := range selectors {
		content := doc.Find(selector).First()
		if content.Length() > 0 {
			text = content.Text()
			break
		}
	}

	// Fallback to body if no article found
	if text == "" {
		text = doc.Find("body").Text()
	}

	// Clean up whitespace
	text = strings.TrimSpace(text)
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	text = strings.Join(cleaned, " ")

	// Limit to ~3000 characters to avoid token limits
	if len(text) > 3000 {
		text = text[:3000] + "..."
	}

	return text, nil
}

func main() {
	token := os.Getenv("TG_BOT_TOKEN")
	chatID := os.Getenv("TG_CHANNEL_ID")
	aiApiToken := os.Getenv("GEMINI_API_TOKEN")
	aiModel := os.Getenv("GEMINI_MODEL")

	if token == "" || chatID == "" {
		fmt.Println("Missing TG_BOT_TOKEN or TG_CHANNEL_ID")
		return
	}

	if aiApiToken == "" || aiModel == "" {
		fmt.Println("Missing GEMINI_API_TOKEN or GEMINI_MODEL")
		return
	}

	ctx := context.Background()
	g := genkit.Init(ctx, genkit.WithPlugins(&googlegenai.GoogleAI{
		APIKey: aiApiToken,
	}))

	state := loadState()
	defer saveState(state) // 🔒 ALWAYS save state

	postsSent := 0

	for _, feedURL := range RSS_FEEDS {
		if postsSent >= MAX_POSTS_PER_RUN {
			fmt.Printf("✅ Reached limit of %d posts, stopping\n", MAX_POSTS_PER_RUN)
			break
		}

		fmt.Printf("📡 Fetching: %s\n", feedURL)

		rss, err := fetchRSS(feedURL)
		if err != nil {
			fmt.Printf("⚠️  RSS feed failed (%s): %v\n", feedURL, err)
			continue // Skip this feed and move to next
		}

		fmt.Printf("   Found %d items\n", len(rss.Channel.Items))

		// Process from oldest to newest
		for i := len(rss.Channel.Items) - 1; i >= 0; i-- {
			if postsSent >= MAX_POSTS_PER_RUN {
				break
			}

			item := rss.Channel.Items[i]
			id := hash(item.Link)

			if state[id] {
				continue
			}

			fmt.Printf("📄 Fetching article content...\n")
			articleContent, fetchErr := fetchArticleContent(item.Link)

			aiDescript := "NO AI DESCRIPTION"
			if fetchErr == nil {
				resp, aiErr := genkit.Generate(ctx, g,
					ai.WithPrompt(fmt.Sprintf(AI_PROMPT, item.Title, articleContent)),
					ai.WithModelName(aiModel),
				)

				if aiErr == nil {
					aiDescript = "\n\n💡 " + convertToTelegramHTML(resp.Text())
				} else {
					fmt.Printf("   ⚠️  AI summary failed: %v\n", aiErr)
				}
			}

			msg := fmt.Sprintf("📰 %s\n%s\n%s", item.Title, item.Link, aiDescript)
			err := sendToTelegram(token, chatID, msg)
			if err == nil {
				state[id] = true
				postsSent++
				fmt.Printf("   ✉️  Sent: %s\n", item.Title)
			} else {
				fmt.Printf("   ⚠️  Send failed, skipping item: %v\n", err)
			}

			time.Sleep(2 * time.Second) // safe pacing
		}
	}

	fmt.Printf("\n🎉 Job finished: %d posts sent\n", postsSent)
}
