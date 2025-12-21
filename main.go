package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
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

const STATE_FILE = "state.json"
const MAX_POSTS_PER_RUN = 10

type RSS struct {
	Channel struct {
		Items []Item `xml:"item"`
	} `xml:"channel"`
}

type Item struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
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
		"chat_id": chatID,
		"text":    text,
	}

	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(string(rb))
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

func main() {
	token := os.Getenv("TG_BOT_TOKEN")
	chatID := os.Getenv("TG_CHANNEL_ID")

	if token == "" || chatID == "" {
		fmt.Println("Missing TG_BOT_TOKEN or TG_CHANNEL_ID")
		return
	}

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

			msg := fmt.Sprintf("📰 %s\n%s", item.Title, item.Link)

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
