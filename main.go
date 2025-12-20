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
		return fmt.Errorf("telegram error: %s", rb)
	}
	return nil
}

func main() {
	token := os.Getenv("TG_BOT_TOKEN")
	chatID := os.Getenv("TG_CHANNEL_ID")

	if token == "" || chatID == "" {
		panic("TG_BOT_TOKEN or TG_CHANNEL_ID missing")
	}

	resp, err := http.Get(RSS_URL)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var rss RSS
	if err := xml.Unmarshal(body, &rss); err != nil {
		panic(err)
	}

	state := loadState()

	for i := len(rss.Channel.Items) - 1; i >= 0; i-- {
		item := rss.Channel.Items[i]
		id := hash(item.Title + item.Link)

		if state[id] {
			continue
		}

		msg := fmt.Sprintf("📰 %s\n%s", item.Title, item.Link)
		if err := sendToTelegram(token, chatID, msg); err != nil {
			panic(err)
		}

		state[id] = true
	}

	saveState(state)
}
