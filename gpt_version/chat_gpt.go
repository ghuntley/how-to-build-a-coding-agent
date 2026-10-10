package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/sashabaranov/go-openai"
)

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
	Function    func(input json.RawMessage) (string, error)
}

func main() {
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	flag.Parse()

	if *verbose {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Println("Verbose logging enabled")
	} else {
		log.SetOutput(os.Stdout)
		log.SetFlags(0)
		log.SetPrefix("")
	}

	// Get API Key from environment variable, use default if not set
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		// Set your ChatAnywhere free API Key here
		apiKey = "YOUR_API_KEY"
	}

	// Use ChatAnywhere proxy service (recommended for China)
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = "https://api.chatanywhere.tech/v1"
	// For international users: config.BaseURL = "https://api.chatanywhere.org/v1"

	client := openai.NewClientWithConfig(config)
	if *verbose {
		log.Println("OpenAI client initialized with ChatAnywhere")
		log.Printf("Base URL: %s", config.BaseURL)
	}

	scanner := bufio.NewScanner(os.Stdin)
	getUserMessage := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}
		return scanner.Text(), true
	}

	agent := NewAgent(client, getUserMessage, nil, *verbose)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}
}

func NewAgent(client *openai.Client, getUserMessage func() (string, bool), tools []ToolDefinition, verbose bool) *Agent {
	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
		tools:          tools,
		verbose:        verbose,
	}
}

type Agent struct {
	client         *openai.Client
	getUserMessage func() (string, bool)
	tools          []ToolDefinition
	verbose        bool
}

func (a *Agent) Run(ctx context.Context) error {
	// Conversation history
	conversation := []openai.ChatCompletionMessage{}

	if a.verbose {
		log.Println("Starting chat session")
	}
	fmt.Println("Chat with GPT (use 'ctrl-c' to quit)")

	for {
		fmt.Print("\u001b[94mYou\u001b[0m: ")
		userInput, ok := a.getUserMessage()
		if !ok {
			if a.verbose {
				log.Println("User input ended, breaking from chat loop")
			}
			break
		}

		// Skip empty messages
		if userInput == "" {
			if a.verbose {
				log.Println("Skipping empty message")
			}
			continue
		}

		if a.verbose {
			log.Printf("User input received: %q", userInput)
		}

		userMessage := openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userInput,
		}
		conversation = append(conversation, userMessage)

		if a.verbose {
			log.Printf("Sending message to GPT, conversation length: %d", len(conversation))
		}

		response, err := a.runInference(ctx, conversation)
		if err != nil {
			if a.verbose {
				log.Printf("Error during inference: %v", err)
			}
			return err
		}
		conversation = append(conversation, response)

		if a.verbose {
			log.Printf("Received response from GPT")
		}

		fmt.Printf("\u001b[93mGPT\u001b[0m: %s\n", response.Content)
	}

	if a.verbose {
		log.Println("Chat session ended")
	}
	return nil
}

func (a *Agent) runInference(ctx context.Context, conversation []openai.ChatCompletionMessage) (openai.ChatCompletionMessage, error) {
	model := openai.GPT3Dot5Turbo
	if a.verbose {
		log.Printf("Making API call to GPT with model: %s", model)
	}

	// Synchronous API call
	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       model,
		Messages:    conversation,
		MaxTokens:   1024,
		Temperature: 0.7,
	})

	if a.verbose {
		if err != nil {
			log.Printf("API call failed: %v", err)
		} else {
			log.Printf("API call successful, response received")
		}
	}

	if err != nil {
		return openai.ChatCompletionMessage{}, err
	}

	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, fmt.Errorf("no response choices returned")
	}

	return resp.Choices[0].Message, nil
}
