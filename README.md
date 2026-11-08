# Loop

A lightweight coding agent for your terminal. Ask it to explore a project, fix bugs, edit files, or run commands. Built for local models, including llama.cpp.

## Get started

Build with Go 1.26.1 or newer:

```sh
go build -o loop .
```

Start Loop with your model server running:

```sh
./loop --endpoint http://localhost:8080/v1 --model local-model
```

Then describe what you want done. You can also pass a task directly:

```sh
./loop "find and fix the failing tests"
```

Use `/config` to change settings. Configuration is saved in `~/.loop/config.json`.

## Everyday use

- **Enter** sends your message.
- **Ctrl+N** add a new line.
- Paste multiple lines without sending them.
- Scroll with the mouse wheel or **PageUp / PageDown**.
- **Ctrl+C** stops the current task.
- Messages sent during a task are queued. **Ctrl+Z** removes the newest waiting message.

## Conversations

Conversations save automatically once they have content. Empty drafts aren't saved.

- `/resume` - choose a conversation with **↑/↓**, then press **Enter** to load it.
- `/new` - start a fresh conversation.
- `/clear` - clear the current conversation.
- `/plan` - view task progress.

To reopen your latest conversation when starting Loop:

```sh
./loop --resume
```

## Useful commands

| Command | What it does |
| --- | --- |
| `/help` | Show available commands |
| `/config` | Change settings |
| `/model` | Choose a model |
| `/tools` | Enable or disable tools |
| `/skills` | View available skills |
| `/nostop` | Choose a work mode and pause interval |
| `/nostop off` | Turn nonstop mode off |
| `/exit` | Quit |

Loop can read and edit files, search code, run background commands, and delegate work to subagents. Skills, plugins, and custom slash commands let you extend it.

Loop runs commands on your machine. Review approvals and use source control; use an isolated environment for untrusted work. `--yes` skips approval prompts.

[MIT license](LICENSE) · Lucian BLETAN
