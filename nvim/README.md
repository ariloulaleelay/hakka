# hakka.nvim

Neovim front-end for the hakka agent. Talks to the TCP gateway
(`127.0.0.1:9876` by default) using newline-delimited JSON.
Streams assistant deltas live into a floating window.

## Install (lazy.nvim)

```lua
{
  dir = "<where you put hakka>/nvim",
  name = "hakka.nvim",
  cmd = { "HakkaChat", "HakkaSend", "HakkaReset" },
  config = function()
    require("hakka").setup({
      addr = "127.0.0.1:9876",
    })
    vim.keymap.set("n", "<leader>hc", "<cmd>HakkaChat<cr>", { desc = "hakka: chat" })
    vim.keymap.set("n", "<leader>hr", "<cmd>HakkaReset<cr>", { desc = "hakka: reset session" })
  end,
}
```

## Usage

| Command            | Action                              |
| ------------------ | ----------------------------------- |
| `:HakkaChat`       | Toggle the chat float (streaming)   |
| `:HakkaSend {msg}` | One-shot send, reply via `vim.notify` |
| `:HakkaReset`      | Drop the current session id         |

Inside the chat float:

- **Default shortcuts** (configurable):
  - `<CR>` (normal) — send
  - `<C-s>` (insert) — send
  - `q` (normal) — close
- `?` (normal) — show all available shortcuts with their source (default/user)

The plugin keeps a single session id per Neovim instance; `:HakkaReset`
clears it so the next request starts a fresh conversation.

### Custom shortcuts

You can define your own shortcuts via the `setup()` config. The key is the
keymap left-hand side (e.g. `"<C-r>"`, `"<C-e>"`, `"<CR>"`) and the value
is a command name. Built-in command names:

| Command    | Action                        |
| ---------- | ----------------------------- |
| `submit`   | Send the current prompt       |
| `close`    | Close the chat window         |

Example — override `<CR>` to also work in insert mode and add a custom
shortcut:

```lua
require("hakka").setup({
  addr = "127.0.0.1:9876",
  shortcuts = {
    ["<C-r>"] = "submit",   -- Ctrl+R to send from insert mode
    ["<C-e>"] = "close",    -- Ctrl+E to close
    -- You can also override defaults:
    ["<CR>"]  = "submit",   -- keep default behaviour
    ["q"]     = "close",    -- keep default behaviour
  },
})
```

User-defined shortcuts appear with a `(user)` tag in the shortcuts popup
(press `?` inside the chat float) and are highlighted differently from
defaults.

## Statusline

The plugin tracks LLM token usage inside a global variable `vim.g.hakka_tokens`. It contains a string like `105 ctx / 15 req`. Every time the tokens are updated, the plugin fires a `User HakkaTokensUpdated` autocommand. 

You can hook this up to your statusline. For example, in **lualine.nvim**:

```lua
require('lualine').setup({
  sections = {
    lualine_x = {
      {
        function() return vim.g.hakka_tokens or "" end,
        icon = '󰚩',
      }
    }
  }
})
```

## Slash commands

These are interpreted server-side, you can type them in the prompt:

| Command                | Effect                                                     |
| ---------------------- | ---------------------------------------------------------- |
| `/help`                | Display the help menu with all available slash commands   |
| `/model list`          | List configured models, mark the active one                |
| `/model show`          | Show currently active model                                |
| `/model switch <name>` | Switch the active model for this session                   |
| `/session list`        | List all active sessions in the database, mark current     |
| `/session create`      | Start a fresh session with a random UUID                  |
| `/session switch <id>` | Switch the Neovim editor window to another session         |
| `/session delete <id>` | Delete specified session from database (clears if active)  |
