# GPT Version Agents

This directory contains GPT-powered versions of the coding agents, converted from the original Anthropic Claude versions. Each file is a standalone program that provides different tool capabilities.

## Files Overview

- `chat_gpt.go` - Basic chat functionality without tools
- `read_gpt.go` - Chat with file reading capability
- `list_files_gpt.go` - Chat with file reading and listing capabilities
- `bash_tool_gpt.go` - Chat with file operations and bash command execution
- `code_search_tool_gpt.go` - Chat with file operations, bash, and code search using ripgrep
- `edit_tool_gpt.go` - Chat with file operations, bash, and file editing capabilities

## Usage

Each program is standalone. To run any of them:

```bash
# For basic chat
go run gpt_version/chat_gpt.go

# For file reading agent
go run gpt_version/read_gpt.go

# For file listing agent
go run gpt_version/list_files_gpt.go

# For bash command agent
go run gpt_version/bash_tool_gpt.go

# For code search agent
go run gpt_version/code_search_tool_gpt.go

# For file editing agent
go run gpt_version/edit_tool_gpt.go
```

## Configuration

All programs use the ChatAnywhere proxy service for accessing OpenAI's API. You can:

1. Set the `OPENAI_API_KEY` environment variable with your API key
2. Or modify the default API key in the source code (line ~32 in each file)

For international users, change the `BaseURL` from `https://api.chatanywhere.tech/v1` to `https://api.chatanywhere.org/v1`.

## Features

### Tools Available

- **read_file**: Read contents of files
- **list_files**: List files and directories 
- **bash**: Execute bash commands
- **code_search**: Search code using ripgrep (rg)
- **edit_file**: Edit files by replacing text

### Verbose Mode

All programs support verbose logging:

```bash
go run gpt_version/edit_tool_gpt.go -verbose
```

## Dependencies

- `github.com/sashabaranov/go-openai` - OpenAI Go SDK
- Standard Go libraries

## Notes

- These are standalone programs, not meant to be compiled together
- The linter warnings about redeclared identifiers are expected since each file defines the same types independently
- Each program provides a progressively more feature-rich agent, choose based on your needs