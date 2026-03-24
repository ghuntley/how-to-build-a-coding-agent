"""
A coding agent built on top of Claude, ported from the Go workshop.

This single file mirrors the incremental Go files (chat.go → read.go →
list_files.go → bash_tool.go → edit_tool.go → code_search_tool.go) but
combines everything into one Python script with verbose comments.

Usage:
    pip install anthropic
    export ANTHROPIC_API_KEY="your-key"
    python agent.py              # normal mode
    python agent.py --verbose    # see detailed logs

Architecture overview:
    1. We define a list of "tools" — each is a dict with a name, description,
       input schema (JSON Schema), and a Python function to execute.
    2. We send messages to Claude along with the tool definitions.
    3. Claude decides whether to respond with plain text OR request a tool call.
    4. If Claude requests a tool, we execute it locally and send the result back.
    5. We keep looping until Claude responds with just text (no more tool calls).
    This is the "agentic tool loop" — the same pattern used by Claude Code,
    ChatGPT plugins, and most LLM agents.
"""

import argparse
import json
import logging
import os
import subprocess
import sys

import anthropic

# ---------------------------------------------------------------------------
# Logging setup
# ---------------------------------------------------------------------------

logger = logging.getLogger("agent")


def setup_logging(verbose: bool) -> None:
    """Configure logging. Verbose mode sends detailed logs to stderr so they
    don't mix with the agent's conversational output on stdout."""
    if verbose:
        logging.basicConfig(
            level=logging.DEBUG,
            format="%(asctime)s %(levelname)s %(filename)s:%(lineno)d  %(message)s",
            stream=sys.stderr,
        )
        logger.debug("Verbose logging enabled")
    else:
        # In normal mode, suppress all log output so the user only sees the
        # chat conversation.
        logging.basicConfig(level=logging.CRITICAL)


# ---------------------------------------------------------------------------
# Tool definitions
# ---------------------------------------------------------------------------
# Each tool is a plain dictionary with four keys:
#   - "name":         identifier Claude uses to request this tool
#   - "description":  natural-language explanation Claude reads to decide
#                     whether to use the tool
#   - "input_schema": a JSON Schema object describing the expected input
#   - "function":     the Python callable we invoke locally when Claude
#                     requests this tool
#
# The Anthropic API sends these schemas to Claude so it knows what arguments
# are valid. Claude then returns a tool_use block whose "input" matches
# this schema.
# ---------------------------------------------------------------------------


# ---- Tool 1: read_file ----------------------------------------------------
# Mirrors read.go — lets Claude read a file's contents.

def read_file(path: str) -> str:
    """Read and return the full contents of a file at *path*."""
    logger.debug("Reading file: %s", path)
    with open(path, "r") as f:
        content = f.read()
    logger.debug("Read %d bytes from %s", len(content), path)
    return content


READ_FILE_TOOL = {
    "name": "read_file",
    "description": (
        "Read the contents of a given relative file path. "
        "Use this when you want to see what's inside a file. "
        "Do not use this with directory names."
    ),
    "input_schema": {
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "description": "The relative path of a file in the working directory.",
            }
        },
        "required": ["path"],
    },
}


# ---- Tool 2: list_files ---------------------------------------------------
# Mirrors list_files.go — lets Claude explore the directory tree.

def list_files(path: str = ".") -> str:
    """Walk *path* recursively and return a JSON array of relative file paths.
    Skips .git/ and .devenv/ directories."""
    logger.debug("Listing files in: %s", path)
    results = []
    for root, dirs, files in os.walk(path):
        # Prune hidden/internal directories so we don't return noise.
        dirs[:] = [d for d in dirs if d not in (".git", ".devenv", "__pycache__")]
        for name in files:
            full = os.path.join(root, name)
            rel = os.path.relpath(full, path)
            results.append(rel)
    logger.debug("Found %d files in %s", len(results), path)
    return json.dumps(results, indent=2)


LIST_FILES_TOOL = {
    "name": "list_files",
    "description": (
        "List files and directories at a given path. "
        "If no path is provided, lists files in the current directory."
    ),
    "input_schema": {
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "description": (
                    "Optional relative path to list files from. "
                    "Defaults to current directory if not provided."
                ),
            }
        },
        "required": [],
    },
}


# ---- Tool 3: bash ---------------------------------------------------------
# Mirrors bash_tool.go — lets Claude run shell commands.

def bash(command: str) -> str:
    """Execute *command* in a bash shell and return combined stdout+stderr."""
    logger.debug("Executing bash command: %s", command)
    result = subprocess.run(
        ["bash", "-c", command],
        capture_output=True,
        text=True,
    )
    output = result.stdout + result.stderr
    if result.returncode != 0:
        logger.debug("Command failed (exit %d): %s", result.returncode, command)
        return f"Command failed (exit {result.returncode}):\n{output}"
    logger.debug("Command succeeded (%d chars output)", len(output))
    return output.strip()


BASH_TOOL = {
    "name": "bash",
    "description": "Execute a bash command and return its output.",
    "input_schema": {
        "type": "object",
        "properties": {
            "command": {
                "type": "string",
                "description": "The bash command to execute.",
            }
        },
        "required": ["command"],
    },
}


# ---- Tool 4: edit_file ----------------------------------------------------
# Mirrors edit_tool.go — lets Claude modify or create files.

def edit_file(path: str, old_str: str, new_str: str) -> str:
    """Replace *old_str* with *new_str* in the file at *path*.

    - If the file doesn't exist and old_str is empty, creates a new file
      with new_str as the content.
    - old_str must appear exactly once in the file (to avoid ambiguous edits).
    - If old_str is empty on an existing file, new_str is appended.
    """
    logger.debug("Editing file: %s", path)

    if old_str == new_str:
        return "Error: old_str and new_str are identical, no change needed."

    # --- Handle file creation ---
    if not os.path.exists(path) and old_str == "":
        logger.debug("File does not exist, creating: %s", path)
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        with open(path, "w") as f:
            f.write(new_str)
        return f"Created new file: {path}"

    # --- Read existing content ---
    with open(path, "r") as f:
        content = f.read()

    # --- Append mode ---
    if old_str == "":
        new_content = content + new_str
    else:
        # --- Replacement mode ---
        count = content.count(old_str)
        if count == 0:
            return "Error: old_str not found in file."
        if count > 1:
            return f"Error: old_str found {count} times — must be unique."
        new_content = content.replace(old_str, new_str, 1)

    with open(path, "w") as f:
        f.write(new_content)
    logger.debug("Successfully edited %s", path)
    return "OK"


EDIT_FILE_TOOL = {
    "name": "edit_file",
    "description": (
        "Make edits to a text file. Replaces 'old_str' with 'new_str' in the "
        "given file. They must be different. If the file doesn't exist and "
        "old_str is empty, a new file is created with new_str as its content."
    ),
    "input_schema": {
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "description": "The path to the file.",
            },
            "old_str": {
                "type": "string",
                "description": (
                    "Text to search for — must match exactly and appear only once."
                ),
            },
            "new_str": {
                "type": "string",
                "description": "Text to replace old_str with.",
            },
        },
        "required": ["path", "old_str", "new_str"],
    },
}


# ---- Tool 5: code_search --------------------------------------------------
# Mirrors code_search_tool.go — pattern search via ripgrep.

def code_search(pattern: str, path: str = ".", file_type: str = "", case_sensitive: bool = False) -> str:
    """Search for *pattern* using ripgrep (rg). Returns matching lines with
    file names and line numbers, capped at 50 results."""
    logger.debug("Searching for pattern: %s", pattern)

    args = ["rg", "--line-number", "--with-filename", "--color=never"]

    if not case_sensitive:
        args.append("--ignore-case")
    if file_type:
        args.extend(["--type", file_type])

    args.append(pattern)
    args.append(path)

    logger.debug("Ripgrep args: %s", args)

    result = subprocess.run(args, capture_output=True, text=True)

    # ripgrep exit code 1 = no matches (not a real error)
    if result.returncode == 1:
        logger.debug("No matches found for: %s", pattern)
        return "No matches found."
    if result.returncode != 0:
        return f"Search failed: {result.stderr}"

    lines = result.stdout.strip().split("\n")
    logger.debug("Found %d matches", len(lines))

    if len(lines) > 50:
        return "\n".join(lines[:50]) + f"\n... (showing 50 of {len(lines)} matches)"
    return "\n".join(lines)


CODE_SEARCH_TOOL = {
    "name": "code_search",
    "description": (
        "Search for code patterns using ripgrep (rg). "
        "Finds function definitions, variable usage, or any text in the codebase."
    ),
    "input_schema": {
        "type": "object",
        "properties": {
            "pattern": {
                "type": "string",
                "description": "The search pattern or regex to look for.",
            },
            "path": {
                "type": "string",
                "description": "Optional path to search in. Defaults to current directory.",
            },
            "file_type": {
                "type": "string",
                "description": "Optional file type to filter by (e.g. 'py', 'go', 'js').",
            },
            "case_sensitive": {
                "type": "boolean",
                "description": "Whether the search should be case sensitive (default: false).",
            },
        },
        "required": ["pattern"],
    },
}


# ---------------------------------------------------------------------------
# Tool registry
# ---------------------------------------------------------------------------
# This is the equivalent of the Go slice:
#   tools := []ToolDefinition{ReadFileDefinition, ListFilesDefinition, ...}
#
# We pair each tool definition (what Claude sees) with the Python function
# (what we execute locally). The key that links them is the tool "name".

TOOLS = [
    (READ_FILE_TOOL,    read_file),
    (LIST_FILES_TOOL,   list_files),
    (BASH_TOOL,         bash),
    (EDIT_FILE_TOOL,    edit_file),
    (CODE_SEARCH_TOOL,  code_search),
]

# Build a name → function lookup dict for fast dispatch.
TOOL_FUNCTIONS = {defn["name"]: func for defn, func in TOOLS}

# The list of definitions we send to the Anthropic API (without the functions).
TOOL_DEFINITIONS = [defn for defn, _ in TOOLS]


# ---------------------------------------------------------------------------
# Execute a single tool call
# ---------------------------------------------------------------------------

def execute_tool(name: str, tool_input: dict) -> str:
    """Look up *name* in the registry and call the corresponding function
    with the arguments Claude provided in *tool_input*.

    This is the Python equivalent of the Go inner loop that scans
    `for _, tool := range a.tools { if tool.Name == toolUse.Name { ... } }`.
    """
    func = TOOL_FUNCTIONS.get(name)
    if func is None:
        error_msg = f"Tool '{name}' not found in registry."
        logger.error(error_msg)
        return error_msg

    try:
        # Unpack the input dict as keyword arguments.
        # e.g. {"path": "main.go"} → read_file(path="main.go")
        return func(**tool_input)
    except Exception as e:
        error_msg = f"Tool '{name}' failed: {e}"
        logger.exception(error_msg)
        return error_msg


# ---------------------------------------------------------------------------
# Agent
# ---------------------------------------------------------------------------

class Agent:
    """A conversational coding agent backed by Claude.

    Mirrors the Go Agent struct. The two important methods are:
      - run()           — the outer REPL loop (get user input → chat)
      - run_inference()  — a single API call to Claude with the current
                          conversation and tool definitions
    """

    def __init__(self, verbose: bool = False):
        # The Anthropic client reads ANTHROPIC_API_KEY from the environment
        # automatically — same as the Go `anthropic.NewClient()`.
        self.client = anthropic.Anthropic()
        self.model = "claude-sonnet-4-20250514"
        self.verbose = verbose

        # The conversation history. Each element is a dict with "role" and
        # "content", following the Messages API format. We accumulate messages
        # here so Claude has full context across turns.
        self.conversation: list[dict] = []

        logger.debug("Agent initialized with model=%s, %d tools", self.model, len(TOOL_DEFINITIONS))

    # ---- Outer REPL loop ---------------------------------------------------

    def run(self) -> None:
        """Main loop: read user input → send to Claude → process response.

        This mirrors `func (a *Agent) Run(ctx)` in the Go code.
        """
        print("Chat with Claude (use ctrl-c to quit)")

        while True:
            # Prompt and read one line from the user.
            try:
                user_input = input("\033[94mYou\033[0m: ")
            except (EOFError, KeyboardInterrupt):
                # ctrl-d or ctrl-c — exit gracefully.
                print("\nGoodbye!")
                break

            if not user_input.strip():
                continue

            logger.debug("User input: %r", user_input)

            # ------------------------------------------------------------------
            # Append the user's message to the conversation history.
            # The Messages API expects alternating user/assistant messages.
            # ------------------------------------------------------------------
            self.conversation.append({
                "role": "user",
                "content": user_input,
            })

            # ------------------------------------------------------------------
            # Send the conversation to Claude and get a response.
            # ------------------------------------------------------------------
            response = self.run_inference()

            # ------------------------------------------------------------------
            # TOOL EVENT LOOP
            #
            # This is the core of the agent. After each Claude response we:
            #   1. Check every content block in the response.
            #   2. Print any text blocks to the user.
            #   3. If there are tool_use blocks, execute each tool and collect
            #      the results.
            #   4. Send all tool results back to Claude as a user message.
            #   5. Repeat from step 1 with Claude's new response.
            #   6. Exit the loop when Claude responds with NO tool_use blocks.
            #
            # This is the Python equivalent of the Go `for { ... if !hasToolUse
            # { break } ... }` inner loop.
            # ------------------------------------------------------------------
            while True:
                tool_results = []   # accumulate results for all tool calls
                has_tool_use = False

                logger.debug("Processing %d content blocks", len(response.content))

                for block in response.content:
                    if block.type == "text":
                        # ---- Plain text from Claude ----
                        # Print it to the user immediately.
                        print(f"\033[93mClaude\033[0m: {block.text}")

                    elif block.type == "tool_use":
                        # ---- Claude is requesting a tool call ----
                        # block.name  = which tool (e.g. "read_file")
                        # block.input = the arguments as a dict (e.g. {"path": "main.go"})
                        # block.id    = unique ID we must reference in our result
                        has_tool_use = True

                        logger.debug("Tool request: %s(%s)", block.name, json.dumps(block.input))
                        print(f"\033[96mtool\033[0m: {block.name}({json.dumps(block.input)})")

                        # Execute the tool locally.
                        result = execute_tool(block.name, block.input)
                        print(f"\033[92mresult\033[0m: {result[:200]}{'...' if len(result) > 200 else ''}")

                        # Package the result in the format the API expects.
                        # The tool_use_id links this result back to the specific
                        # tool_use block that requested it.
                        tool_results.append({
                            "type": "tool_result",
                            "tool_use_id": block.id,
                            "content": result,
                        })

                # If Claude didn't ask for any tools, we're done — break out
                # of the tool loop and go back to waiting for user input.
                if not has_tool_use:
                    break

                # ---- Send tool results back to Claude ----
                # Tool results are sent as a "user" message containing one or
                # more tool_result blocks. Claude then processes the results
                # and may respond with more text, more tool calls, or both.
                logger.debug("Sending %d tool results back to Claude", len(tool_results))
                self.conversation.append({
                    "role": "user",
                    "content": tool_results,
                })

                # Get Claude's next response (it might request more tools).
                response = self.run_inference()

    # ---- Single inference call ---------------------------------------------

    def run_inference(self) -> anthropic.types.Message:
        """Make one API call to Claude with the full conversation and tools.

        Mirrors `func (a *Agent) runInference(ctx, conversation)` in Go.

        Returns the raw Message object. The caller inspects .content for
        text blocks and tool_use blocks.
        """
        logger.debug(
            "API call: model=%s, messages=%d, tools=%d",
            self.model, len(self.conversation), len(TOOL_DEFINITIONS),
        )

        response = self.client.messages.create(
            model=self.model,
            max_tokens=1024,
            tools=TOOL_DEFINITIONS,       # ← tell Claude what tools exist
            messages=self.conversation,    # ← full conversation history
        )

        logger.debug("API response: stop_reason=%s, blocks=%d", response.stop_reason, len(response.content))

        # Append Claude's response to the conversation so subsequent calls
        # include it in the history. We convert it to the dict format the
        # API expects for an "assistant" message.
        self.conversation.append({
            "role": "assistant",
            "content": response.content,
        })

        return response


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="A coding agent powered by Claude")
    parser.add_argument("--verbose", action="store_true", help="Enable verbose debug logging")
    args = parser.parse_args()

    setup_logging(args.verbose)

    agent = Agent(verbose=args.verbose)
    agent.run()


if __name__ == "__main__":
    main()
