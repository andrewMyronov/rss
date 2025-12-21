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
	"strings"
	"time"
)

const RSS_URL = "https://habr.com/ru/rss/articles/"
const STATE_FILE = "state.json"

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

func main() {
	token := os.Getenv("TG_BOT_TOKEN")
	chatID := os.Getenv("TG_CHANNEL_ID")

	if token == "" || chatID == "" {
		fmt.Println("Missing TG_BOT_TOKEN or TG_CHANNEL_ID")
		return
	}

	resp, err := http.Get(RSS_URL)
	if err != nil {
		fmt.Println("RSS fetch failed:", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var rss RSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		fmt.Println("RSS parse failed:", err)
		return
	}

	state := loadState()
	defer saveState(state) // 🔒 ALWAYS save state

	for i := len(rss.Channel.Items) - 1; i >= 0; i-- {
		item := rss.Channel.Items[i]
		id := hash(item.Link) // more stable than title+link

		if state[id] {
			continue
		}

		msg := fmt.Sprintf("📰 %s\n%s", item.Title, item.Link)

		for {
			err := sendToTelegram(token, chatID, msg)
			if err == nil {
				state[id] = true
				break
			}

			// Handle Telegram rate limit
			if strings.Contains(err.Error(), "retry_after") {
				fmt.Println("Rate limited, sleeping 35s...")
				time.Sleep(35 * time.Second)
				continue
			}

			// Log and SKIP (do not crash job)
			fmt.Println("Send failed, skipping item:", err)
			break
		}

		time.Sleep(2 * time.Second) // safe pacing
	}

	fmt.Println("Job finished successfully")
}
