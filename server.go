package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

type Server struct {
	client     *anthropic.Client
	tools      []ToolDefinition
	verbose    bool
	sessions   map[string][]anthropic.MessageParam
	sessionsMu sync.RWMutex
	model      anthropic.Model
	maxTokens  int64
	userStore  *UserStore
}

type MessageRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type Event struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Result string          `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type MessageResponse struct {
	Events []Event `json:"events"`
	Final  string  `json:"final"`
}

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	modelFlag := flag.String("model", "", "override model (default: env ANTHROPIC_MODEL or Claude 3.7 Sonnet)")
	maxTokensFlag := flag.Int("max_tokens", 1024, "max tokens for responses")
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

	client := anthropic.NewClient()
	if *verbose {
		log.Println("Anthropic client initialized")
	}

	// Resolve model and max tokens from flags/env
	model := anthropic.Model(strings.TrimSpace(*modelFlag))
	if model == "" {
		model = anthropic.Model(strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")))
	}
	if model == "" {
		model = anthropic.ModelClaude3_7SonnetLatest
	}

	maxTokens := int64(*maxTokensFlag)
	if envMax := strings.TrimSpace(os.Getenv("MAX_TOKENS")); envMax != "" {
		if v, err := strconv.Atoi(envMax); err == nil {
			maxTokens = int64(v)
		}
	}

	tools := []ToolDefinition{ReadFileDefinition, ListFilesDefinition, BashDefinition, EditFileDefinition, CodeSearchDefinition}
	if *verbose {
		log.Printf("Initialized %d tools", len(tools))
	}

	// Initialize user store
	userStorePath := strings.TrimSpace(os.Getenv("USER_STORE_PATH"))
	if userStorePath == "" {
		userStorePath = "data/users.json"
	}
	us, err := NewUserStore(userStorePath)
	if err != nil {
		log.Fatalf("failed to init user store: %v", err)
	}

	s := &Server{
		client:    &client,
		tools:     tools,
		verbose:   *verbose,
		sessions:  make(map[string][]anthropic.MessageParam),
		model:     model,
		maxTokens: maxTokens,
		userStore: us,
	}

	// Init Stripe if configured
	s.initStripeFromEnv()

	http.HandleFunc("/api/session", s.handleNewSession)
	http.HandleFunc("/api/message", s.handleMessage)

	// Auth endpoints
	http.HandleFunc("/api/signup", s.handleSignup)
	http.HandleFunc("/api/login", s.handleLogin)
	http.HandleFunc("/api/me", s.handleMe)

	// Billing endpoints
	http.HandleFunc("/api/checkout", s.handleCheckout)
	http.HandleFunc("/api/portal", s.handlePortal)
	http.HandleFunc("/api/stripe/webhook", s.handleStripeWebhook)

	// Serve static UI from ./web
	fs := http.FileServer(http.Dir("web"))
	http.Handle("/", fs)

	log.Printf("Server listening on %s (model=%s, max_tokens=%d)", *addr, s.model, s.maxTokens)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func (s *Server) handleNewSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := generateID()
	s.sessionsMu.Lock()
	s.sessions[id] = []anthropic.MessageParam{}
	s.sessionsMu.Unlock()
	writeJSON(w, map[string]string{"session_id": id})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// Require authenticated and subscribed user
	user, err := s.getUserFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}
	if !user.SubscriptionOK {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "subscription required"})
		return
	}
	defer r.Body.Close()
	var req MessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Message) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session_id and message are required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	resp, err := s.processUserMessage(ctx, req.SessionID, req.Message)
	if err != nil {
		if s.verbose {
			log.Printf("process error: %v", err)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, resp)
}

// -----------
// Auth & Utils
// -----------

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" || strings.TrimSpace(req.Password) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "email and password required"})
		return
	}
	user, err := s.userStore.CreateUser(req.Email, req.Password)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	claims := JWTClaims{Subject: user.ID, Email: user.Email, Exp: time.Now().Add(30 * 24 * time.Hour).Unix()}
	tok, err := createJWT(claims)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to create token"})
		return
	}
	writeJSON(w, map[string]any{"token": tok, "user": map[string]any{"email": user.Email, "subscription_ok": user.SubscriptionOK}})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Email) == "" || strings.TrimSpace(req.Password) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "email and password required"})
		return
	}
	user, err := s.userStore.Authenticate(req.Email, req.Password)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
		return
	}
	claims := JWTClaims{Subject: user.ID, Email: user.Email, Exp: time.Now().Add(30 * 24 * time.Hour).Unix()}
	tok, err := createJWT(claims)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to create token"})
		return
	}
	writeJSON(w, map[string]any{"token": tok, "user": map[string]any{"email": user.Email, "subscription_ok": user.SubscriptionOK}})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, err := s.getUserFromRequest(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, map[string]any{"email": user.Email, "subscription_ok": user.SubscriptionOK})
}

func (s *Server) getUserFromRequest(r *http.Request) (*User, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" { return nil, fmt.Errorf("no auth header") }
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") { return nil, fmt.Errorf("invalid auth header") }
	claims, err := parseAndValidateJWT(parts[1])
	if err != nil { return nil, err }
	user := s.userStore.GetByEmail(claims.Email)
	if user == nil { return nil, fmt.Errorf("user not found") }
	return user, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) processUserMessage(ctx context.Context, sessionID, userInput string) (*MessageResponse, error) {
	s.sessionsMu.RLock()
	conversation := append([]anthropic.MessageParam(nil), s.sessions[sessionID]...)
	s.sessionsMu.RUnlock()

	var events []Event
	finalText := ""

	userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
	conversation = append(conversation, userMessage)

	message, err := s.runInference(ctx, conversation)
	if err != nil {
		return nil, err
	}
	conversation = append(conversation, message.ToParam())

	for {
		var toolResults []anthropic.ContentBlockParamUnion
		hasToolUse := false

		for _, content := range message.Content {
			switch content.Type {
			case "text":
				events = append(events, Event{Type: "assistant", Text: content.Text})
				finalText += content.Text + "\n"
			case "tool_use":
				hasToolUse = true
				toolUse := content.AsToolUse()
				if s.verbose {
					log.Printf("Tool use: %s input=%s", toolUse.Name, string(toolUse.Input))
				}
				events = append(events, Event{Type: "tool", Tool: toolUse.Name, Input: toolUse.Input})

				var toolResult string
				var toolErr error
				var found bool
				for _, tool := range s.tools {
					if tool.Name == toolUse.Name {
						toolResult, toolErr = tool.Function(toolUse.Input)
						found = true
						break
					}
				}
				if !found {
					toolErr = fmt.Errorf("tool '%s' not found", toolUse.Name)
				}

				if toolErr != nil {
					events = append(events, Event{Type: "error", Tool: toolUse.Name, Error: toolErr.Error()})
					toolResults = append(toolResults, anthropic.NewToolResultBlock(toolUse.ID, toolErr.Error(), true))
				} else {
					events = append(events, Event{Type: "result", Tool: toolUse.Name, Result: toolResult})
					toolResults = append(toolResults, anthropic.NewToolResultBlock(toolUse.ID, toolResult, false))
				}
			}
		}

		if !hasToolUse {
			break
		}

		toolResultMessage := anthropic.NewUserMessage(toolResults...)
		conversation = append(conversation, toolResultMessage)

		message, err = s.runInference(ctx, conversation)
		if err != nil {
			return nil, err
		}
		conversation = append(conversation, message.ToParam())
	}

	// Save updated conversation
	s.sessionsMu.Lock()
	s.sessions[sessionID] = conversation
	s.sessionsMu.Unlock()

	return &MessageResponse{Events: events, Final: strings.TrimSpace(finalText)}, nil
}

func (s *Server) runInference(ctx context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	anthropicTools := []anthropic.ToolUnionParam{}
	for _, tool := range s.tools {
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: tool.InputSchema,
			},
		})
	}

	if s.verbose {
		log.Printf("Calling Claude (model=%s, tools=%d)", s.model, len(anthropicTools))
	}

	message, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model,
		MaxTokens: s.maxTokens,
		Messages:  conversation,
		Tools:     anthropicTools,
	})

	if s.verbose {
		if err != nil {
			log.Printf("API error: %v", err)
		} else {
			log.Printf("API call successful")
		}
	}

	return message, err
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// -----------------------
// Tooling implementation
// -----------------------

type ToolDefinition struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	InputSchema anthropic.ToolInputSchemaParam `json:"input_schema"`
	Function    func(input json.RawMessage) (string, error)
}

var ReadFileDefinition = ToolDefinition{
	Name:        "read_file",
	Description: "Read the contents of a given relative file path. Use this when you want to see what's inside a file. Do not use this with directory names.",
	InputSchema: ReadFileInputSchema,
	Function:    ReadFile,
}

var ListFilesDefinition = ToolDefinition{
	Name:        "list_files",
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory.",
	InputSchema: ListFilesInputSchema,
	Function:    ListFiles,
}

var BashDefinition = ToolDefinition{
	Name:        "bash",
	Description: "Execute a bash command and return its output. Use this to run shell commands.",
	InputSchema: BashInputSchema,
	Function:    Bash,
}

var EditFileDefinition = ToolDefinition{
	Name: "edit_file",
	Description: `Make edits to a text file.

Replaces 'old_str' with 'new_str' in the given file. 'old_str' and 'new_str' MUST be different from each other.

If the file specified with path doesn't exist, it will be created.
`,
	InputSchema: EditFileInputSchema,
	Function:    EditFile,
}

var CodeSearchDefinition = ToolDefinition{
	Name:        "code_search",
	Description: "Search for code patterns using ripgrep (rg).",
	InputSchema: CodeSearchInputSchema,
	Function:    CodeSearch,
}

type ReadFileInput struct {
	Path string `json:"path" jsonschema_description:"The relative path of a file in the working directory."`
}

var ReadFileInputSchema = GenerateSchema[ReadFileInput]()

type ListFilesInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"Optional relative path to list files from. Defaults to current directory if not provided."`
}

var ListFilesInputSchema = GenerateSchema[ListFilesInput]()

type BashInput struct {
	Command string `json:"command" jsonschema_description:"The bash command to execute."`
}

var BashInputSchema = GenerateSchema[BashInput]()

type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and must only have one match exactly"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}

var EditFileInputSchema = GenerateSchema[EditFileInput]()

type CodeSearchInput struct {
	Pattern       string `json:"pattern" jsonschema_description:"The search pattern or regex to look for"`
	Path          string `json:"path,omitempty" jsonschema_description:"Optional path to search in (file or directory)"`
	FileType      string `json:"file_type,omitempty" jsonschema_description:"Optional file extension to limit search to (e.g., 'go', 'js', 'py')"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema_description:"Whether the search should be case sensitive (default: false)"`
}

var CodeSearchInputSchema = GenerateSchema[CodeSearchInput]()

func ReadFile(input json.RawMessage) (string, error) {
	readFileInput := ReadFileInput{}
	if err := json.Unmarshal(input, &readFileInput); err != nil {
		return "", err
	}
	content, err := os.ReadFile(readFileInput.Path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func ListFiles(input json.RawMessage) (string, error) {
	listFilesInput := ListFilesInput{}
	if err := json.Unmarshal(input, &listFilesInput); err != nil {
		return "", err
	}
	dir := "."
	if listFilesInput.Path != "" {
		dir = listFilesInput.Path
	}
	var files []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if info.IsDir() && (relPath == ".devenv" || strings.HasPrefix(relPath, ".devenv/")) {
			return filepath.SkipDir
		}
		if relPath != "." {
			if info.IsDir() {
				files = append(files, relPath+"/")
			} else {
				files = append(files, relPath)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func Bash(input json.RawMessage) (string, error) {
	bashInput := BashInput{}
	if err := json.Unmarshal(input, &bashInput); err != nil {
		return "", err
	}
	cmd := exec.Command("bash", "-c", bashInput.Command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Command failed with error: %s\nOutput: %s", err.Error(), string(output)), nil
	}
	return strings.TrimSpace(string(output)), nil
}

func EditFile(input json.RawMessage) (string, error) {
	editFileInput := EditFileInput{}
	if err := json.Unmarshal(input, &editFileInput); err != nil {
		return "", err
	}
	if editFileInput.Path == "" || editFileInput.OldStr == editFileInput.NewStr {
		return "", fmt.Errorf("invalid input parameters")
	}
	content, err := os.ReadFile(editFileInput.Path)
	if err != nil {
		if os.IsNotExist(err) && editFileInput.OldStr == "" {
			return createNewFile(editFileInput.Path, editFileInput.NewStr)
		}
		return "", err
	}
	oldContent := string(content)
	var newContent string
	if editFileInput.OldStr == "" {
		newContent = oldContent + editFileInput.NewStr
	} else {
		count := strings.Count(oldContent, editFileInput.OldStr)
		if count == 0 {
			return "", fmt.Errorf("old_str not found in file")
		}
		if count > 1 {
			return "", fmt.Errorf("old_str found %d times in file, must be unique", count)
		}
		newContent = strings.Replace(oldContent, editFileInput.OldStr, editFileInput.NewStr, 1)
	}
	if err := os.WriteFile(editFileInput.Path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func createNewFile(filePathStr, content string) (string, error) {
	dir := path.Dir(filePathStr)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}
	if err := os.WriteFile(filePathStr, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	return fmt.Sprintf("Successfully created file %s", filePathStr), nil
}

func CodeSearch(input json.RawMessage) (string, error) {
	codeSearchInput := CodeSearchInput{}
	if err := json.Unmarshal(input, &codeSearchInput); err != nil {
		return "", err
	}
	if codeSearchInput.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	// Prefer ripgrep if available
	if _, err := exec.LookPath("rg"); err == nil {
		args := []string{"rg", "--line-number", "--with-filename", "--color=never"}
		if !codeSearchInput.CaseSensitive {
			args = append(args, "--ignore-case")
		}
		if codeSearchInput.FileType != "" {
			args = append(args, "--type", codeSearchInput.FileType)
		}
		args = append(args, codeSearchInput.Pattern)
		if codeSearchInput.Path != "" {
			args = append(args, codeSearchInput.Path)
		} else {
			args = append(args, ".")
		}
		cmd := exec.Command(args[0], args[1:]...)
		output, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return "No matches found", nil
			}
			return "", fmt.Errorf("search failed: %w", err)
		}
		result := strings.TrimSpace(string(output))
		lines := strings.Split(result, "\n")
		if len(lines) > 50 {
			result = strings.Join(lines[:50], "\n") + fmt.Sprintf("\n... (showing first 50 of %d matches)", len(lines))
		}
		return result, nil
	}
	// Fallback minimal grep if ripgrep not available
	args := []string{"grep", "-R", "-n"}
	if !codeSearchInput.CaseSensitive {
		args = append(args, "-i")
	}
	args = append(args, codeSearchInput.Pattern)
	if codeSearchInput.Path != "" {
		args = append(args, codeSearchInput.Path)
	} else {
		args = append(args, ".")
	}
	cmd := exec.Command(args[0], args[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) == 0 {
			return "", err
		}
	}
	result := strings.TrimSpace(string(output))
	lines := strings.Split(result, "\n")
	if len(lines) > 50 {
		result = strings.Join(lines[:50], "\n") + fmt.Sprintf("\n... (showing first 50 of %d matches)", len(lines))
	}
	return result, nil
}

func GenerateSchema[T any]() anthropic.ToolInputSchemaParam {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}
	var v T
	schema := reflector.Reflect(v)
	return anthropic.ToolInputSchemaParam{
		Properties: schema.Properties,
	}
}

