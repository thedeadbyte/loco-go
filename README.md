# loco — LOcal COde

An agentic coding CLI for local open-weight models, in the spirit of Claude
Code. Streams markdown, calls tools (read/edit/write files, grep, fetch a URL,
shell with approval prompts), and keeps per-machine profiles so each of your
computers launches with the right model automatically.

Runs entirely locally against [Ollama](https://ollama.com). Works on Windows,
Linux, and macOS.

This is the Go build of loco: **one self-contained executable**. No Python, no
pip, no virtualenv, nothing to install alongside it.

## Requirements

- [Ollama](https://ollama.com) installed and running
- A tool-capable model. The Qwen2.5-Coder family works best. **Judgment scales
  with model size** — a 7B or 14B follows instructions and picks tools far more
  reliably than a 3B, so use the largest model your hardware runs at a bearable
  speed. Tiny models (3B and under) are fine for quick edits and Q&A but will
  sometimes misjudge when to use a tool.

Run `loco doctor` at any time to check that Ollama, your model, and your config
are all set up correctly.

## Safety

loco is an agent with real access to your machine: it can read and write files
and run shell commands in whatever directory you launch it from. File writes,
edits, and shell commands always prompt for your approval first (unless you pass
`--yolo`). Still, treat it like any tool with shell access — run it in a project
directory, read the diffs and commands before approving, and don't `--yolo` in a
directory you care about. No warranty; see LICENSE.

## Install

### Windows

Download `loco-<version>-windows-amd64.exe` from the releases page, rename it to
`loco.exe`, and drop it somewhere on your `PATH` (e.g. `%LOCALAPPDATA%\Programs\loco\`).
That's the whole install — it is a single static executable with no runtime
dependencies.

Use [Windows Terminal](https://aka.ms/terminal) for the best rendering (it's the
default terminal on Windows 11).

```powershell
# Ollama: installer from https://ollama.com/download/windows
# (or: winget install Ollama.Ollama) — it runs in the tray and finds your
# NVIDIA GPU automatically with the standard driver installed.

loco doctor     # confirm Ollama, model, and config
loco            # start a session
```

On Windows the model's shell tool is PowerShell instead of bash, and config
lives in `%APPDATA%\loco\config.toml`. Everything else is identical, including
profiles — so your Windows and Linux machines share the same muscle memory.

### Linux / macOS

Download the binary for your platform, `chmod +x` it, and move it onto your
`PATH`:

```bash
chmod +x loco-*-linux-amd64
sudo mv loco-*-linux-amd64 /usr/local/bin/loco
loco doctor
```

On an NVIDIA machine, install the driver first (`sudo ubuntu-drivers install`,
then reboot) — Ollama picks up the GPU automatically, no CUDA toolkit needed.
Check the CPU/GPU split after a query with `ollama ps`.

### From source

Needs Go 1.24+:

```bash
git clone https://github.com/thedeadbyte/loco-go && cd loco-go
make build          # ./loco
make install        # into $(go env GOPATH)/bin
make dist           # every platform's binary into dist/, with SHA256SUMS
```

`make dist` cross-compiles from any host — you can build the Windows `.exe` on
Linux, because nothing here uses cgo.

## Profiles (per-machine defaults)

Save a profile on each machine and bind it to that machine's hostname:

```bash
# on a CPU-only laptop
loco profile save laptop -m qwen2.5-coder:7b --ctx 8192 --bind-host

# on a workstation with a GPU
loco profile save workstation -m qwen2.5-coder:14b --ctx 16384 --bind-host
```

After that, plain `loco` on either machine picks its own profile — no flags
needed. Other commands:

```bash
loco profile list          # show profiles, active one is marked →
loco profile use NAME      # fallback profile when no hostname matches
loco profile delete NAME
loco -p workstation        # force a profile for one run
```

Config lives in `~/.config/loco/config.toml` (or `%APPDATA%\loco\config.toml`)
and is safe to hand-edit — keys loco doesn't recognize are preserved when it
rewrites the file.

## Use

```bash
loco                       # interactive session
loco "why does make fail"  # one-shot
loco -m qwen2.5-coder:3b   # override model just this run
loco --yolo                # skip approval prompts (careful)
```

Inside the session: `/help`, `/model NAME`, `/profile [NAME]`, `/theme [NAME]`,
`/context`, `/compact`, `/init`, `/memory`, `/allow`, `/clear`, `/save [file]`,
`/tools`, `/chat`, `/doctor`, `/quit`. The model can read files, search, edit,
and run shell commands — anything destructive asks you first.

### Approvals & diffs

Before an edit or write runs, loco shows a colored **diff** of the exact change
so you approve with full context, not a byte count. At each prompt you can
answer `y` (once), `a` (allow that tool for the rest of the session — kills the
repetitive prompting on things like `pip install`), or `N`. `/allow` shows
what's currently auto-approved. `--yolo` skips prompts entirely.

### Context management

The bottom bar shows live context usage, and after each reply loco prints a
`⚡ tok/s · tokens · ctx` line (handy for comparing 7B vs 14B speed). As the
window fills, loco automatically summarizes older turns so you don't silently
lose the start of the conversation; `/compact` does it on demand and `/context`
shows the gauge.

**Ctrl-C** during a reply cancels that turn and keeps the session — the partial
answer stays on screen. **Ctrl-D** quits.

### Project memory

Drop a **LOCO.md** in your project (or run `/init` to scaffold one) and loco
loads it into the model's system prompt every session — the best way to keep a
small local model following your conventions. `/memory` reloads it after edits.

### @-mentions

Prefix a path with `@` (e.g. `explain @src/app.py`) to pull that file's contents
straight into your message.

### Themes

`/theme` with no argument previews the color schemes; `/theme NAME` switches and
remembers your choice. Built-ins: `orchid` (default), `matrix`, `amber`,
`ocean`, `mono`, `sunset`. You can also start with `loco --theme matrix`.

Some local models print their tool calls as raw JSON instead of using Ollama's
structured tool channel. loco executes those correctly and hides the JSON from
the transcript, so you just see the clean `⏺ tool` summary either way.

If a model isn't downloaded yet, loco offers to pull it with a progress bar.

## Model suggestions

| Hardware | Model | Why |
|---|---|---|
| CPU-only laptop | `qwen2.5-coder:3b` | snappy; light edits and Q&A |
| CPU-only laptop | `qwen2.5-coder:7b` | best quality still usable on CPU |
| GPU with ~6 GB VRAM | `qwen2.5-coder:7b` | ~4.7 GB, fits mostly in VRAM |
| GPU + system RAM | `qwen2.5-coder:14b` | split GPU/RAM, slower but stronger |
| 32+ GB RAM | `qwen2.5-coder:32b` | runs on CPU/RAM; expect ~2-4 tok/s |

Tool calling works best with the Qwen coder family; `llama3.1:8b` also works.

## Differences from the Python build

loco was originally written in Python; this is a behavior-compatible port. Both
read the same `config.toml`, so profiles carry over. Deliberate differences:

- **`grep` uses Go's RE2 syntax**, which has no backreferences or lookaround.
  Everything else about the pattern language is the same.
- **Ctrl-C during a reply cancels cleanly** and returns you to the prompt,
  instead of relying on an interrupt landing in the right place.
- **A bad `/profile NAME` no longer exits the session** — it prints the error and
  leaves you where you were.
- **Tool arguments are coerced, not rejected**: a model that sends `5` where the
  schema says `"5"` gets the call it meant, and unknown extra arguments are
  ignored rather than failing the call.
- **Input history is a plain text file**, not prompt_toolkit's format, so the two
  builds don't share `history` (they do share `config.toml`).
- Long answers commit finished markdown blocks to the scrollback as they stream,
  rather than re-rendering the whole answer on every frame.

## License

MIT — see [LICENSE](LICENSE).

## Contributing

Issues and pull requests welcome. loco is intentionally small — a handful of
single-purpose packages under `internal/` (agent loop, tools, Ollama client,
terminal UI, config/profiles). `make check` runs the tests, gofmt, and vet on
both Linux and Windows targets.
