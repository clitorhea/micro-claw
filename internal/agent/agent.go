package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"micro-claw/internal/config"
	"micro-claw/internal/metrics"
	"micro-claw/internal/telegram"
	"micro-claw/internal/tools"
)

type Part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

type FunctionCall struct {
	Name       string                 `json:"name"`
	Args       map[string]interface{} `json:"args"`
	ToolCallID string                 `json:"toolCallId,omitempty"`
}

type FunctionResponse struct {
	Name       string                 `json:"name"`
	Response   map[string]interface{} `json:"response"`
	ToolCallID string                 `json:"toolCallId,omitempty"`
}

type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

type SystemInstruction struct {
	Parts []Part `json:"parts"`
}

type GenerateContentRequest struct {
	Contents          []Content          `json:"contents"`
	Tools             []tools.Tool       `json:"tools,omitempty"`
	SystemInstruction *SystemInstruction `json:"systemInstruction,omitempty"`
}

type Candidate struct {
	Content      Content `json:"content"`
	FinishReason string  `json:"finishReason"`
}

type GenerateContentResponse struct {
	Candidates []Candidate `json:"candidates"`
}

// OpenAI / DeepSeek API Structs
type OpenAIMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIFunctionDecl `json:"function"`
}

type OpenAIFunctionDecl struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Parameters  tools.FunctionParameter `json:"parameters"`
}

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`
	Tools    []OpenAITool    `json:"tools,omitempty"`
}

type OpenAIResponse struct {
	Choices []OpenAIChoice `json:"choices"`
}

type OpenAIChoice struct {
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type LogEntry struct {
	Timestamp string      `json:"timestamp"`
	Event     string      `json:"event"`
	Data      interface{} `json:"data"`
}

type Agent struct {
	cfg        *config.Config
	tgClient   *telegram.Client
	registry   *tools.Registry
	history    []Content
	historyMu  sync.Mutex
	httpClient *http.Client
}

func NewAgent(cfg *config.Config, tgClient *telegram.Client, registry *tools.Registry) *Agent {
	return &Agent{
		cfg:      cfg,
		tgClient: tgClient,
		registry: registry,
		history:  make([]Content, 0),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (a *Agent) Config() *config.Config {
	return a.cfg
}

func (a *Agent) ClearHistory() {
	a.historyMu.Lock()
	a.history = make([]Content, 0)
	a.historyMu.Unlock()
	a.logToFile("history_reset", map[string]string{"status": "success"})
}

func (a *Agent) logToFile(event string, data interface{}) {
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Event:     event,
		Data:      data,
	}

	file, err := os.OpenFile(a.cfg.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[Agent] Failed to open log file %s: %v", a.cfg.LogFilePath, err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(entry); err != nil {
		log.Printf("[Agent] Failed to write log entry: %v", err)
	}
}

func (a *Agent) HandleUserMessage(ctx context.Context, text string) {
	log.Printf("[Agent] Received message from user: %s", text)
	a.logToFile("user_input", map[string]string{"text": text})

	a.historyMu.Lock()
	a.history = append(a.history, Content{
		Role:  "user",
		Parts: []Part{{Text: text}},
	})
	a.historyMu.Unlock()

	a.runConversationLoop(ctx)
}

func (a *Agent) HandleAnomaly(ctx context.Context, anomalyPayload string) {
	log.Printf("[Agent] Processing anomaly alert: %s", anomalyPayload)
	a.logToFile("anomaly_alert", map[string]string{"payload": anomalyPayload})

	prompt := fmt.Sprintf("SYSTEM ALERT: The following performance/resource anomaly was detected on the NAS:\n\n%s\n\nPlease evaluate this data and suggest or take appropriate troubleshooting steps (e.g. check container stats or logs, restart service if needed).", anomalyPayload)

	a.historyMu.Lock()
	a.history = append(a.history, Content{
		Role:  "user",
		Parts: []Part{{Text: prompt}},
	})
	a.historyMu.Unlock()

	a.runConversationLoop(ctx)
}

func (a *Agent) runConversationLoop(ctx context.Context) {
	const maxIterations = 5

	for i := 0; i < maxIterations; i++ {
		a.pruneHistory()

		log.Printf("[Agent] Calling LLM (%s - iteration %d/%d)...", a.cfg.LLMProvider, i+1, maxIterations)
		responseContent, err := a.callLLM(ctx)
		if err != nil {
			log.Printf("[Agent] LLM API error: %v", err)
			
			// Local offline diagnostic report fallback
			fallbackMsg := fmt.Sprintf("⚠️ *LLM Service Offline* (%v)\n\nI was unable to consult the AI router. Running direct system diagnosis...\n\n", err)
			cpu, mem, disk, sysErr := metrics.GetSystemMetrics()
			if sysErr == nil {
				topProc, _ := metrics.GetTopProcesses()
				fallbackMsg += fmt.Sprintf("*Current Load Metrics:*\n- CPU: %.2f%%\n- Memory: %.2f%%\n- Disk Space: %.2f%%\n\n*Top Processes:*\n```\n%s\n```\n", cpu, mem, disk, topProc)
			} else {
				fallbackMsg += fmt.Sprintf("❌ *Failed to fetch diagnostics:* %v\n", sysErr)
			}
			_, _ = a.tgClient.SendMessage(ctx, a.cfg.TelegramUserID, fallbackMsg)
			return
		}

		if responseContent == nil || len(responseContent.Parts) == 0 {
			log.Println("[Agent] Received empty response from LLM")
			return
		}

		// Log and add LLM response to history
		a.logToFile("llm_response", responseContent)
		a.historyMu.Lock()
		a.history = append(a.history, *responseContent)
		a.historyMu.Unlock()

		hasFunctionCalls := false
		var toolResponses []Part

		for _, part := range responseContent.Parts {
			if part.FunctionCall != nil {
				hasFunctionCalls = true
				fc := part.FunctionCall
				log.Printf("[Agent] LLM requested tool execution: %s", fc.Name)

				// Execute tool using registry
				output, err := a.registry.Execute(ctx, fc.Name, fc.Args)
				if err != nil {
					output = fmt.Sprintf("Error executing tool %s: %v", fc.Name, err)
				}

				a.logToFile("tool_output", map[string]string{
					"name":   fc.Name,
					"output": output,
				})

				toolResponses = append(toolResponses, Part{
					FunctionResponse: &FunctionResponse{
						Name: fc.Name,
						Response: map[string]interface{}{
							"output": output,
						},
						ToolCallID: fc.ToolCallID,
					},
				})
			}
		}

		if hasFunctionCalls {
			// Append tool outputs as the next message (role: user) and continue loop
			a.historyMu.Lock()
			a.history = append(a.history, Content{
				Role:  "user",
				Parts: toolResponses,
			})
			a.historyMu.Unlock()
			continue
		}

		// If no tool calls, it must be a final text response. Send it to the user.
		for _, part := range responseContent.Parts {
			if part.Text != "" {
				log.Printf("[Agent] Sending text response to Telegram user: %s", part.Text)
				_, err := a.tgClient.SendMessage(ctx, a.cfg.TelegramUserID, part.Text)
				if err != nil {
					log.Printf("[Agent] Failed to send Telegram message: %v", err)
				}
				a.logToFile("agent_text_response", map[string]string{"text": part.Text})
			}
		}
		return
	}

	log.Printf("[Agent] Reached max iterations limit (%d)", maxIterations)
	_, _ = a.tgClient.SendMessage(ctx, a.cfg.TelegramUserID, "⚠️ *Limit Exceeded:* Conversation exceeded maximum tool execution depth.")
}

func (a *Agent) callLLM(ctx context.Context) (*Content, error) {
	if a.cfg.LLMProvider == "gemini" {
		return a.callGemini(ctx)
	}
	return a.callOpenAI(ctx)
}

func (a *Agent) callGemini(ctx context.Context) (*Content, error) {
	a.historyMu.Lock()
	contentsCopy := make([]Content, len(a.history))
	copy(contentsCopy, a.history)
	a.historyMu.Unlock()

	reqPayload := GenerateContentRequest{
		Contents: contentsCopy,
		Tools:    a.registry.GetToolsSchema(),
		SystemInstruction: &SystemInstruction{
			Parts: []Part{
				{
					Text: "You are MicroClaw, a hyper-lightweight, autonomous NAS System Assistant and Watchdog.\n" +
						"You help the user manage storage, Docker containers, and cron scheduling, perform online research (via crawling/scraping docs), and troubleshoot host metrics.\n" +
						"You have access to local commands, cron scheduling, and web scraping tools. When you decide to call a state-changing tool (like restarting/stopping containers, scheduling tasks), it will trigger a manual approval query via Telegram inline buttons. Once the user approves or rejects, you will receive the outcome as the tool's return value.\n" +
						"Expert engineering behavior:\n" +
						"- Analyze metrics anomalies, top processes, and container logs to diagnose issues.\n" +
						"- Recommend, configure, or adjust periodic tasks (cronjobs) via scheduling tools to automate operations.\n" +
						"- Browse online documentations or websites using web_scrape and web_crawl to find troubleshooting procedures, API usage, or instructions.\n" +
						"- Format all metrics, lists, tables, and code beautifully in markdown. Be concise, technical, and highly actionable.",
				},
			},
		},
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("%s%s:generateContent?key=%s", a.cfg.LLMAPIURL, a.cfg.LLMModel, a.cfg.LLMAPIKey)
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Gemini API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var respPayload GenerateContentResponse
	if err := json.NewDecoder(resp.Body).Decode(&respPayload); err != nil {
		return nil, err
	}

	if len(respPayload.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates returned by Gemini API")
	}

	candidate := respPayload.Candidates[0]
	return &candidate.Content, nil
}

func (a *Agent) callOpenAI(ctx context.Context) (*Content, error) {
	a.historyMu.Lock()
	contentsCopy := make([]Content, len(a.history))
	copy(contentsCopy, a.history)
	a.historyMu.Unlock()

	systemPrompt := "You are MicroClaw, a hyper-lightweight, autonomous NAS System Assistant and Watchdog.\n" +
		"You help the user manage storage, Docker containers, and cron scheduling, perform online research (via crawling/scraping docs), and troubleshoot host metrics.\n" +
		"You have access to local commands, cron scheduling, and web scraping tools. When you decide to call a state-changing tool (like restarting/stopping containers, scheduling tasks), it will trigger a manual approval query via Telegram inline buttons. Once the user approves or rejects, you will receive the outcome as the tool's return value.\n" +
		"Expert engineering behavior:\n" +
		"- Analyze metrics anomalies, top processes, and container logs to diagnose issues.\n" +
		"- Recommend, configure, or adjust periodic tasks (cronjobs) via scheduling tools to automate operations.\n" +
		"- Browse online documentations or websites using web_scrape and web_crawl to find troubleshooting procedures, API usage, or instructions.\n" +
		"- Format all metrics, lists, tables, and code beautifully in markdown. Be concise, technical, and highly actionable."

	openaiMessages := []OpenAIMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}
	openaiMessages = append(openaiMessages, convertHistoryToOpenAI(contentsCopy)...)

	reqPayload := OpenAIRequest{
		Model:    a.cfg.LLMModel,
		Messages: openaiMessages,
		Tools:    convertToolsToOpenAI(a.registry.GetToolsSchema()),
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	apiURL := a.cfg.LLMAPIURL
	if !strings.HasSuffix(apiURL, "/chat/completions") {
		if strings.HasSuffix(apiURL, "/") {
			apiURL += "chat/completions"
		} else {
			apiURL += "/chat/completions"
		}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.LLMAPIKey)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var respPayload OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&respPayload); err != nil {
		return nil, err
	}

	if len(respPayload.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned by OpenAI/DeepSeek API")
	}

	choice := respPayload.Choices[0]
	return convertOpenAIResponseToContent(choice.Message), nil
}

func convertHistoryToOpenAI(history []Content) []OpenAIMessage {
	var messages []OpenAIMessage
	for _, c := range history {
		role := c.Role
		if role == "model" {
			role = "assistant"
		}

		isToolResponse := false
		for _, part := range c.Parts {
			if part.FunctionResponse != nil {
				isToolResponse = true
				break
			}
		}

		if isToolResponse {
			for _, part := range c.Parts {
				if part.FunctionResponse != nil {
					toolCallID := part.FunctionResponse.ToolCallID
					if toolCallID == "" {
						toolCallID = "call_" + part.FunctionResponse.Name
					}
					respBytes, _ := json.Marshal(part.FunctionResponse.Response)
					messages = append(messages, OpenAIMessage{
						Role:       "tool",
						ToolCallID: toolCallID,
						Content:    string(respBytes),
					})
				}
			}
		} else {
			var textParts []string
			var toolCalls []OpenAIToolCall

			for _, part := range c.Parts {
				if part.Text != "" {
					textParts = append(textParts, part.Text)
				}
				if part.FunctionCall != nil {
					argsBytes, _ := json.Marshal(part.FunctionCall.Args)
					toolCalls = append(toolCalls, OpenAIToolCall{
						ID:   part.FunctionCall.ToolCallID,
						Type: "function",
						Function: OpenAIFunctionCall{
							Name:      part.FunctionCall.Name,
							Arguments: string(argsBytes),
						},
					})
				}
			}

			contentStr := ""
			if len(textParts) > 0 {
				contentStr = strings.Join(textParts, "\n")
			}

			msg := OpenAIMessage{
				Role:    role,
				Content: contentStr,
			}
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
			}
			messages = append(messages, msg)
		}
	}
	return messages
}

func convertOpenAIResponseToContent(openaiMsg OpenAIMessage) *Content {
	content := &Content{
		Role: "model",
	}

	if openaiMsg.Content != "" {
		content.Parts = append(content.Parts, Part{Text: openaiMsg.Content})
	}

	for _, tc := range openaiMsg.ToolCalls {
		if tc.Type == "function" {
			var args map[string]interface{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			content.Parts = append(content.Parts, Part{
				FunctionCall: &FunctionCall{
					Name:       tc.Function.Name,
					Args:       args,
					ToolCallID: tc.ID,
				},
			})
		}
	}
	return content
}

func convertToolsToOpenAI(geminiTools []tools.Tool) []OpenAITool {
	var openaiTools []OpenAITool
	for _, t := range geminiTools {
		for _, fd := range t.FunctionDeclarations {
			openaiTools = append(openaiTools, OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDecl{
					Name:        fd.Name,
					Description: fd.Description,
					Parameters:  convertParamsToOpenAI(fd.Parameters),
				},
			})
		}
	}
	return openaiTools
}

func convertParamsToOpenAI(param tools.FunctionParameter) tools.FunctionParameter {
	newParam := tools.FunctionParameter{
		Type:        strings.ToLower(param.Type),
		Description: param.Description,
		Required:    param.Required,
	}
	if len(param.Properties) > 0 {
		newParam.Properties = make(map[string]tools.FunctionParameter)
		for k, v := range param.Properties {
			newParam.Properties[k] = convertParamsToOpenAI(v)
		}
	}
	return newParam
}

func (a *Agent) pruneHistory() {
	a.historyMu.Lock()
	defer a.historyMu.Unlock()

	const maxHistorySize = 16
	if len(a.history) <= maxHistorySize {
		return
	}

	excess := len(a.history) - maxHistorySize
	startIdx := excess
	for startIdx < len(a.history) && a.history[startIdx].Role != "user" {
		startIdx++
	}

	if startIdx < len(a.history) {
		a.history = a.history[startIdx:]
	} else {
		a.history = a.history[len(a.history)-maxHistorySize:]
		if len(a.history) > 0 {
			a.history[0].Role = "user"
		}
	}
}
