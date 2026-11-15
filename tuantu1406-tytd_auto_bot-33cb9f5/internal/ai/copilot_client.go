package ai

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

const (
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	CopilotBaseURL  = "https://api.individual.githubcopilot.com"
)

// TokenResponse represents the token response from GitHub
type TokenResponse struct {
	Token string `json:"token"`
}

// TokenManager manages Copilot token lifecycle with automatic refresh
type TokenManager struct {
	APIKey       string
	CurrentToken string
	mu           sync.Mutex
}

// NewTokenManager creates a new token manager
func NewTokenManager(apiKey string) *TokenManager {
	return &TokenManager{
		APIKey: apiKey,
	}
}

// GetToken retrieves a valid token, refreshing if necessary
func (tm *TokenManager) GetToken() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.CurrentToken == "" {
		tm.CurrentToken = tm.fetchToken()
		return tm.CurrentToken
	}

	// Check expiration
	// Token format: tid=...;exp=1734855555;...
	parts := strings.Split(tm.CurrentToken, ";exp=")
	if len(parts) < 2 {
		tm.CurrentToken = tm.fetchToken()
		return tm.CurrentToken
	}

	expStr := strings.Split(parts[1], ";")[0]
	expTimestamp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		tm.CurrentToken = tm.fetchToken()
		return tm.CurrentToken
	}

	expTime := time.Unix(expTimestamp, 0)
	// Refresh if expired or about to expire (within 60 seconds)
	if time.Now().After(expTime.Add(-60 * time.Second)) {
		tm.CurrentToken = tm.fetchToken()
	}

	return tm.CurrentToken
}

// fetchToken fetches a new token from GitHub
func (tm *TokenManager) fetchToken() string {
	req, err := http.NewRequest("GET", CopilotTokenURL, nil)
	if err != nil {
		log.Printf("Error creating token request: %v\n", err)
		return ""
	}

	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("sec-ch-ua-platform", "\"Windows\"")
	req.Header.Set("authorization", "Bearer "+tm.APIKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Code/1.103.2 Chrome/132.0.6834.210 Electron/34.3.2 Safari/537.36")
	req.Header.Set("sec-ch-ua", "\"Not A(Brand\";v=\"8\", \"Chromium\";v=\"132\"")
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "vscode-file://vscode-app")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Accept-Language", "en-US")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error fetching token: %v\n", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		var tokenResp TokenResponse
		if err := json.Unmarshal(body, &tokenResp); err == nil {
			return tokenResp.Token
		}
	} else {
		log.Printf("Failed to get token. Status: %d\n", resp.StatusCode)
	}
	return ""
}

// CopilotTransport is a custom HTTP transport that adds required headers
type CopilotTransport struct {
	Transport http.RoundTripper
}

// RoundTrip adds custom headers to each request
func (t *CopilotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("editor-version", "vscode/1.103.2")
	req.Header.Set("Copilot-Vision-Request", "true")
	return t.Transport.RoundTrip(req)
}

// CopilotClient is the main client for interacting with GitHub Copilot
type CopilotClient struct {
	TokenManager *TokenManager
}

// NewCopilotClient creates a new Copilot client
func NewCopilotClient(apiKey string) *CopilotClient {
	return &CopilotClient{
		TokenManager: NewTokenManager(apiKey),
	}
}

// ChatCompletion sends a chat completion request to Copilot
func (c *CopilotClient) ChatCompletion(messages []ChatMessage) (string, error) {
	// Get valid token
	token := c.TokenManager.GetToken()
	if token == "" {
		return "", nil
	}

	// Configure OpenAI client for Copilot
	config := openai.DefaultConfig(token)
	config.BaseURL = CopilotBaseURL
	config.HTTPClient = &http.Client{
		Transport: &CopilotTransport{Transport: http.DefaultTransport},
	}
	client := openai.NewClientWithConfig(config)

	// Convert ChatMessage to openai.ChatCompletionMessage
	openaiMessages := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		var role string
		switch msg.Role {
		case "system":
			role = openai.ChatMessageRoleSystem
		case "user":
			role = openai.ChatMessageRoleUser
		case "assistant":
			role = openai.ChatMessageRoleAssistant
		default:
			role = openai.ChatMessageRoleUser
		}
		openaiMessages[i] = openai.ChatCompletionMessage{
			Role:    role,
			Content: msg.Content,
		}
	}

	// Make API call
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    "gpt-4o",
			Messages: openaiMessages,
		},
	)

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", nil
	}

	return resp.Choices[0].Message.Content, nil
}
