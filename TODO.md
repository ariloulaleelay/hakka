# Epics

- [x] Opensource ready
  - [x] Read tokens to config from environment
  - [x] Remove tokens and keys from commits
  - [x] Remove "hidden" keywords
  - [ ] Add more logs (I want to see what happens during session and at what state we are)
  - [x] Better readme
- [ ] Stability
  - [ ] If user reports problem, fix states, to maybe detect error.
- [ ] Telegram gateway
  - [x] Separate different users sessions from each other, sessin commands need to be chat aware, or event different session classes.
  - [x] Limit telegram usage by whitelist of chat ids
  - [x] Limit tools, that llm can use in telegram mode
  - [ ] Chat with multiple users, how to show it properly
  - [ ] Change system prompt for telegram (no cwd info)
  - [ ] Make http_get safer
  - [x] Better display on client (session info, tokens count, markdown)
  - [ ] Response in group chats
    - [x] In a group chat form message with author info in heading (@login + Name)
    - [x] Silently listen group chat (respond only on mentions)
- [ ] Web server
  - [ ] Add handle to read files (we can show images in chat)
- [ ] Tool improvements
  - [x] Get access to mcp servers
  - [x] Add tool controls, disable tools by default
  - [x] Add tool groups/tags, easier control
  - [x] Change tool tags.
  - [x] Add tool enable/disable mechanism
  - [ ] Add dangerous tools confirmations
  - [ ] Add tools confirmation
  - [x] remove mcp prefix from tools
  - [x] Add tool to discover and enable tools
  - [ ] Bugfix: [Truncated: N bytes ommited] should be [TRUNCATED N bytes left] or propely calculate ommited bytes
  - [ ] Automatically join chains of the same tool with different page size.
  - [ ] Llm report bug tool (for example ommited and offset in read file does not work as intended)
  - [x] Destructive tool checkpoints (make temporary backup for files before tool call), return instructions of how to revert specific commands.
  - [x] Implement discovery tool.
  - [x] Revisit discovery tool and check if it works great
- [ ] System prompts managemet
  - [ ] System prompt storage
  - [ ] Commands to list/add/delete/enable/disable system prompts
- [ ] Improve sessions
  - [x] Session autorename
  - [x] Session manual rename
  - [x] Tools to access to sessions
  - [ ] Tools for session manipulation (search, summarize, tags) — requires further research and decomposition
    - [ ] Better session compactification prompt
    - [x] Session search
    - [x] Session summarize
    - [ ] Session tags
  - [x] Better session persistence (dedicated table for messages, not a single json blob)
  - [x] Support PostgreSQL in addition to SQLite (URL-based DB: `sqlite:path`, `postgres://...`)
  - [ ] Advanced session manipulation — on the fly context compression, guided context compression, stashes, forks
      - [ ] Configurable session compression strategies
      - [x] Guided session compression
      - [ ] Simple session compression
- [ ] Create detachable core library
  - [ ] Extract system core
  - [ ] Move project to new core
  - [ ] Create dedicated generic agent
  - [ ] Create dedicated telegram agent
- [x] Add standalone agentic mode (batch run without human, for real autonomous tasks)
  - [ ] Better logging in batch mode (I want to see what actually happens)
- [ ] Show user balance for current provider (how much money left)
- [ ] Make end to end feature implementation:
  - [ ] Establish code and architecture quality control (self-improvement loop)
  - [ ] Implement end to end tests. When I fully automate engine improvement, i need real tests, to be sure nothing breaks.
  - [ ] Implement full cycle automatic feature implementation.
  - [ ] Describe full loop of implementing new feature
    - [ ] Decomposition
    - [ ] Feature implementation
    - [ ] Testing
    - [ ] End to end testing
    - [ ] Live testing
    - [ ] Architecutral refine
    - [ ] Code practice refine
    - [ ] Fix new contracts

# Minor features

- [x] Fix annoying glitch with first message (no newline) — initial `### you` prompt now has a trailing blank line so cursor starts on its own line, not on the header
- [x] Command processor should intercept all commands starting with `/` so typos would not leat to LLM
- [x] Client could send current working directory to session, and session should keep it. It shoud appear in tools context. Now cwd is server's cwd.
- [x] Send full command parameters, and shrink them on client (cleaner). In other words, do not strip snippet on server side.
- [x] After implementing cwd, parameters length increased. Need to solve this issue. And add message to system prompt that all tools follow user's cwd.
- [x] Move from examples to server
- [x] OpenAi adapter, want reasonable retry on _error: error, status code: 429, status: 429 Too Many Requests, message: _
- [x] Shell tool should follow cwd convention.
- [ ] Discover should follow .arcignore, .gitignore rules


# Bugs
- [x] After creating session, `### you` appears twice: `### you\n\n### you\n`
- [x] Discover tool fails, need to debug
- [ ] User can switch to unexistent session (it creates new session)
- [x] Autorename does not work
- [x] When gemini prematurely stops by max tokens on tool call generation we save broken tool call to the session and it breaks completely

# Minor bugs
- [ ] Snippet stripping for `\n` makes one character longer for each backslash. And tabs make string appear longer too.
- [x] Remove protocol_version from wire
- [ ] Migrate from client_cwd to cwd on the wire

# Architecture
- [x] Refactor environment substitution to all text fields in config (now it is some hacks)

