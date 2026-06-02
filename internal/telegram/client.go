package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type Client struct {
	token            string
	authorizedUserID int64
	client           *http.Client
	baseURL          string
}

func NewClient(token string, authorizedUserID int64) *Client {
	return &Client{
		token:            token,
		authorizedUserID: authorizedUserID,
		client: &http.Client{
			Timeout: 40 * time.Second, // Must be slightly larger than polling timeout
		},
		baseURL: fmt.Sprintf("https://api.telegram.org/bot%s", token),
	}
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) (*Message, error) {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	return c.post(ctx, "sendMessage", payload)
}

func (c *Client) SendApprovalKeyboard(ctx context.Context, chatID int64, text string, actionID string) (*Message, error) {
	keyboard := InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Approve ✅", CallbackData: fmt.Sprintf("approve:%s", actionID)},
				{Text: "Reject ❌", CallbackData: fmt.Sprintf("reject:%s", actionID)},
			},
		},
	}
	payload := map[string]interface{}{
		"chat_id":      chatID,
		"text":         text,
		"parse_mode":   "Markdown",
		"reply_markup": keyboard,
	}
	return c.post(ctx, "sendMessage", payload)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string) error {
	payload := map[string]interface{}{
		"callback_query_id": callbackQueryID,
		"text":              text,
	}
	_, err := c.post(ctx, "answerCallbackQuery", payload)
	return err
}

func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string) error {
	payload := map[string]interface{}{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	_, err := c.post(ctx, "editMessageText", payload)
	return err
}

func (c *Client) StartPolling(ctx context.Context, msgChan chan<- *Message, callbackChan chan<- *CallbackQuery) {
	var offset int64 = 0
	log.Printf("[Telegram] Starting long polling for authorized user: %d", c.authorizedUserID)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Telegram] Stopping polling due to context cancellation")
			return
		default:
			updates, err := c.getUpdates(ctx, offset, 30)
			if err != nil {
				log.Printf("[Telegram] Error fetching updates: %v, retrying in 5 seconds...", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			for _, update := range updates {
				if update.UpdateID >= offset {
					offset = update.UpdateID + 1
				}

				// Handle incoming messages
				if update.Message != nil {
					if update.Message.From.ID != c.authorizedUserID {
						log.Printf("[Telegram] Ignoring message from unauthorized user: %d (%s)", update.Message.From.ID, update.Message.From.Username)
						continue
					}
					select {
					case msgChan <- update.Message:
					case <-ctx.Done():
						return
					}
				}

				// Handle callback queries
				if update.CallbackQuery != nil {
					if update.CallbackQuery.From.ID != c.authorizedUserID {
						log.Printf("[Telegram] Ignoring callback query from unauthorized user: %d (%s)", update.CallbackQuery.From.ID, update.CallbackQuery.From.Username)
						continue
					}
					select {
					case callbackChan <- update.CallbackQuery:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}
}

func (c *Client) getUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	u, err := url.Parse(fmt.Sprintf("%s/getUpdates", c.baseURL))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	q.Set("timeout", strconv.Itoa(timeout))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	var apiResp struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.OK {
		return nil, fmt.Errorf("telegram API returned OK=false")
	}

	return apiResp.Result, nil
}

func (c *Client) post(ctx context.Context, method string, payload interface{}) (*Message, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	reqUrl := fmt.Sprintf("%s/%s", c.baseURL, method)
	req, err := http.NewRequestWithContext(ctx, "POST", reqUrl, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("telegram API %s returned status %d: %s", method, resp.StatusCode, errResp.Description)
	}

	var apiResp struct {
		OK     bool     `json:"ok"`
		Result *Message `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}

	if !apiResp.OK {
		return nil, fmt.Errorf("telegram API response OK=false")
	}

	return apiResp.Result, nil
}
