package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/sashabaranov/go-openai"
)

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

	tools := []ToolDefinition{ReadFileDefinition, ListFilesDefinition, BashDefinition, CodeSearchDefinition}
	if *verbose {
		log.Printf("Initialized %d tools", len(tools))
	}
	agent := NewAgent(client, getUserMessage, tools, *verbose)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
	}
}

func NewAgent(
	client *openai.Client,
	getUserMessage func() (string, bool),
	tools []ToolDefinition,
	verbose bool,
) *Agent {
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
	conversation := []openai.ChatCompletionMessage{}

	if a.verbose {
		log.Println("Starting chat session with tools enabled")
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

		message, err := a.runInference(ctx, conversation)
		if err != nil {
			if a.verbose {
				log.Printf("Error during inference: %v", err)
			}
			return err
		}
		conversation = append(conversation, message)

		// Keep processing until GPT stops using tools
		for {
			// Check if there are tool calls in the message
			if len(message.ToolCalls) == 0 {
				// No tool calls, just print the message and break
				if message.Content != "" {
					fmt.Printf("\u001b[93mGPT\u001b[0m: %s\n", message.Content)
				}
				break
			}

			// Process tool calls
			if a.verbose {
				log.Printf("Processing %d tool calls from GPT", len(message.ToolCalls))
			}

			var toolMessages []openai.ChatCompletionMessage
			for _, toolCall := range message.ToolCalls {
				if a.verbose {
					log.Printf("Tool call detected: %s with arguments: %s", toolCall.Function.Name, toolCall.Function.Arguments)
				}
				fmt.Printf("\u001b[96mtool\u001b[0m: %s(%s)\n", toolCall.Function.Name, toolCall.Function.Arguments)

				// Find and execute the tool
				var toolResult string
				var toolError error
				var toolFound bool
				for _, tool := range a.tools {
					if tool.Name == toolCall.Function.Name {
						if a.verbose {
							log.Printf("Executing tool: %s", tool.Name)
						}
						toolResult, toolError = tool.Function(json.RawMessage(toolCall.Function.Arguments))
						fmt.Printf("\u001b[92mresult\u001b[0m: %s\n", toolResult)
						if toolError != nil {
							fmt.Printf("\u001b[91merror\u001b[0m: %s\n", toolError.Error())
						}
						if a.verbose {
							if toolError != nil {
								log.Printf("Tool execution failed: %v", toolError)
							} else {
								log.Printf("Tool execution successful, result length: %d chars", len(toolResult))
							}
						}
						toolFound = true
						break
					}
				}

				if !toolFound {
					toolError = fmt.Errorf("tool '%s' not found", toolCall.Function.Name)
					fmt.Printf("\u001b[91merror\u001b[0m: %s\n", toolError.Error())
				}

				// Add tool result message
				var content string
				if toolError != nil {
					content = fmt.Sprintf("Error: %s", toolError.Error())
				} else {
					content = toolResult
				}

				toolMessage := openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    content,
					ToolCallID: toolCall.ID,
				}
				toolMessages = append(toolMessages, toolMessage)
			}

			// Add all tool messages to conversation
			conversation = append(conversation, toolMessages...)

			if a.verbose {
				log.Printf("Sending %d tool results back to GPT", len(toolMessages))
			}

			// Get GPT's response after tool execution
			message, err = a.runInference(ctx, conversation)
			if err != nil {
				if a.verbose {
					log.Printf("Error during followup inference: %v", err)
				}
				return err
			}
			conversation = append(conversation, message)

			if a.verbose {
				log.Printf("Received followup response from GPT")
			}

			// Continue loop to process the new message
		}
	}

	if a.verbose {
		log.Println("Chat session ended")
	}
	return nil
}

func (a *Agent) runInference(ctx context.Context, conversation []openai.ChatCompletionMessage) (openai.ChatCompletionMessage, error) {
	// Convert tools to OpenAI function definitions
	var functions []openai.FunctionDefinition
	for _, tool := range a.tools {
		functions = append(functions, openai.FunctionDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}

	model := openai.GPT3Dot5Turbo
	if a.verbose {
		log.Printf("Making API call to GPT with model: %s and %d tools", model, len(functions))
	}

	req := openai.ChatCompletionRequest{
		Model:       model,
		Messages:    conversation,
		MaxTokens:   1024,
		Temperature: 0.7,
	}

	// Add tools if available
	if len(functions) > 0 {
		var tools []openai.Tool
		for _, fn := range functions {
			tools = append(tools, openai.Tool{
				Type:     openai.ToolTypeFunction,
				Function: &fn,
			})
		}
		req.Tools = tools
		req.ToolChoice = "auto"
	}

	// Synchronous API call
	resp, err := a.client.CreateChatCompletion(ctx, req)

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

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
	Function    func(input json.RawMessage) (string, error)
}

var ReadFileDefinition = ToolDefinition{
	Name:        "read_file",
	Description: "Read the contents of a given relative file path. Use this when you want to see what's inside a file. Do not use this with directory names.",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "The relative path of a file in the working directory.",
			},
		},
		"required": []string{"path"},
	},
	Function: ReadFile,
}

var ListFilesDefinition = ToolDefinition{
	Name:        "list_files",
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory.",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional relative path to list files from. Defaults to current directory if not provided.",
			},
		},
		"required": []string{},
	},
	Function: ListFiles,
}

var BashDefinition = ToolDefinition{
	Name:        "bash",
	Description: "Execute a bash command and return its output. Use this to run shell commands.",
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The bash command to execute.",
			},
		},
		"required": []string{"command"},
	},
	Function: Bash,
}

var CodeSearchDefinition = ToolDefinition{
	Name: "code_search",
	Description: `Search for code patterns using ripgrep (rg).

Use this to find code patterns, function definitions, variable usage, or any text in the codebase.
You can search by pattern, file type, or directory.`,
	Parameters: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The search pattern or regex to look for",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to search in (file or directory)",
			},
			"file_type": map[string]interface{}{
				"type":        "string",
				"description": "Optional file extension to limit search to (e.g., 'go', 'js', 'py')",
			},
			"case_sensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether the search should be case sensitive (default: false)",
			},
		},
		"required": []string{"pattern"},
	},
	Function: CodeSearch,
}

type ReadFileInput struct {
	Path string `json:"path"`
}

type ListFilesInput struct {
	Path string `json:"path,omitempty"`
}

type BashInput struct {
	Command string `json:"command"`
}

type CodeSearchInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path,omitempty"`
	FileType      string `json:"file_type,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
}

func ReadFile(input json.RawMessage) (string, error) {
	readFileInput := ReadFileInput{}
	err := json.Unmarshal(input, &readFileInput)
	if err != nil {
		panic(err)
	}

	log.Printf("Reading file: %s", readFileInput.Path)
	content, err := os.ReadFile(readFileInput.Path)
	if err != nil {
		log.Printf("Failed to read file %s: %v", readFileInput.Path, err)
		return "", err
	}
	log.Printf("Successfully read file %s (%d bytes)", readFileInput.Path, len(content))
	return string(content), nil
}

func ListFiles(input json.RawMessage) (string, error) {
	listFilesInput := ListFilesInput{}
	err := json.Unmarshal(input, &listFilesInput)
	if err != nil {
		panic(err)
	}

	dir := "."
	if listFilesInput.Path != "" {
		dir = listFilesInput.Path
	}

	log.Printf("Listing files in directory: %s", dir)
	cmd := exec.Command("find", dir, "-type", "f", "-not", "-path", "*/.devenv/*", "-not", "-path", "*/.git/*")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to list files in %s: %v", dir, err)
		return "", err
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		files = []string{}
	}

	result, err := json.Marshal(files)
	if err != nil {
		return "", err
	}

	log.Printf("Successfully listed %d files in %s", len(files), dir)
	return string(result), nil
}

func Bash(input json.RawMessage) (string, error) {
	bashInput := BashInput{}
	err := json.Unmarshal(input, &bashInput)
	if err != nil {
		return "", err
	}

	log.Printf("Executing bash command: %s", bashInput.Command)
	cmd := exec.Command("bash", "-c", bashInput.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Bash command failed: %v", err)
		return fmt.Sprintf("Command failed with error: %s\nOutput: %s", err.Error(), string(output)), nil
	}

	log.Printf("Bash command executed successfully, output length: %d chars", len(output))
	return strings.TrimSpace(string(output)), nil
}

func CodeSearch(input json.RawMessage) (string, error) {
	codeSearchInput := CodeSearchInput{}
	err := json.Unmarshal(input, &codeSearchInput)
	if err != nil {
		return "", err
	}

	if codeSearchInput.Pattern == "" {
		log.Printf("CodeSearch failed: pattern is required")
		return "", fmt.Errorf("pattern is required")
	}

	log.Printf("Searching for pattern: %s", codeSearchInput.Pattern)

	// Build ripgrep command
	args := []string{"rg", "--line-number", "--with-filename", "--color=never"}

	// Add case sensitivity flag
	if !codeSearchInput.CaseSensitive {
		args = append(args, "--ignore-case")
	}

	// Add file type filter if specified
	if codeSearchInput.FileType != "" {
		args = append(args, "--type", codeSearchInput.FileType)
	}

	// Add pattern
	args = append(args, codeSearchInput.Pattern)

	// Add path if specified
	if codeSearchInput.Path != "" {
		args = append(args, codeSearchInput.Path)
	} else {
		args = append(args, ".")
	}

	cmd := exec.Command(args[0], args[1:]...)
	output, err := cmd.Output()

	// ripgrep returns exit code 1 when no matches are found, which is not an error
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			log.Printf("No matches found for pattern: %s", codeSearchInput.Pattern)
			return "No matches found", nil
		}
		log.Printf("Ripgrep command failed: %v", err)
		return "", fmt.Errorf("search failed: %w", err)
	}

	result := strings.TrimSpace(string(output))
	lines := strings.Split(result, "\n")

	log.Printf("Found %d matches for pattern: %s", len(lines), codeSearchInput.Pattern)

	// Limit output to prevent overwhelming responses
	if len(lines) > 50 {
		result = strings.Join(lines[:50], "\n") + fmt.Sprintf("\n... (showing first 50 of %d matches)", len(lines))
	}

	return result, nil
}
