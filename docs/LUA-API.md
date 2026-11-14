# Lua scripting reference (`squat.*`)

SquatClient embeds Lua 5.1 as a sandboxed scripting language alongside the
[TinyFugue-style scripting language](help:scripting). Scripts are plain `.lua` files.

## 1. Basics

- Everything lives in the global table `squat`. Each plugin gets its **own** global environment: globals you
  assign are private to your plugin, and changing `string`, `table` or `math` never affects other plugins.
- All scripts run on one thread, one at a time. You never need locks.
- Every call into your code (a trigger, a handler, a timer tick, loading the file) has a **budget**
  (about 5 million VM instructions and 500 ms wall-clock by default). Exceeding it aborts that call with an
  error in the log; the client keeps running and your other handlers still work.
- Errors in one plugin never stop another plugin.

### The `live` rule (important)

A line can be **live** (just arrived) or **replayed** (scrollback/backlog re-processed after reattaching).
Replayed lines only get *display effects*: `gag`, highlight colours, and spans returned by `line` handlers.
On replayed lines:

- trigger `fn` callbacks are **not called**;
- `line`/`prompt` handlers *are* called (with `l.live == false`) so they can restyle or gag, but
  `squat.send`, `echo`, `notify`, `tf`, `var.set`, `var.del`, `after`, `timer`, `on`, `trigger`, `alias` and
  `remove` do nothing and return `false`.

Write line handlers so they compute the display result from the text, and put side effects in trigger `fn`s.

## 2. Sandbox

Available: base functions (`pairs`, `pcall`, `error`, `setmetatable`, `tostring`, `unpack` ...), `string`,
`table`, `math`, `coroutine`, and a restricted `os` with only `time`, `date`, `clock`, `difftime`
(using the client's clock). `print(...)` goes to the log.

Not available: `io`, `debug`, `package`, `os.execute/remove/rename/getenv/exit`, `dofile`, `loadfile`,
`string.dump`, `getfenv/setfenv`, `module`, `newproxy`, native modules. `loadstring`/`load` work but compile into your
own environment and refuse binary chunks. `string.rep` results are capped (4 MiB).

`require("name")` loads `name.lua` (dots become `/`) from **your plugin's own directory** only.

## 3. Output and sending

| Call | Meaning |
|---|---|
| `squat.send(text [, world])` | Send a line to the game. Returns `true`, or `nil, err`. |
| `squat.echo(x [, world])` | Show text locally. `x` is a string, one span `{s="..."}`, or an array of spans/strings. |
| `squat.notify(title [, body])` | Desktop notification. |
| `squat.log(text [, level])` | Write to the client log; level is `"info"`, `"warn"` or `"error"`. |
| `squat.world()` | Display name of the world the current line/event belongs to. |
| `squat.now()` | Seconds since the Unix epoch (float) from the client clock. |

`world` defaults to the world of the line/event being handled (timers remember the world they were created in).
Calling `send`/`echo`/`tf` with no current world and no `world` argument is an error.

### Spans and colours

A span is a table: `{s="text", fg=..., bg=..., b=true, dim=, i=, u=, st=, blink=, rev=, link="url"}`.

- `fg`/`bg` is an integer **0-255** (xterm-256 palette index; 0-15 follow the theme) or a string
  **`"#rrggbb"`** for 24-bit truecolor. Both survive the round trip to the display exactly; no quantisation.
- Anything else (`256`, `1.5`, `"red"`, `"#abc"`) is an error.

## 4. Events: `squat.on(event, fn [, priority])`

| Event | `fn` receives |
|---|---|
| `line` | `{text, spans, world, prompt, live}` |
| `prompt` | the same, only for flushed prompts |
| `connect`, `disconnect` | `{world, ...}` (more fields depending on the event) |
| `gmcp` | `{package, data, raw, world}` (`data` is the decoded JSON) |
| `input` | `{text, world}`; return `true` to swallow the input |
| `timer` | a table describing the tick |

Line handlers may modify the line:

```lua
squat.on("line", function(l)
  if l.text:find("spam") then return {gag = true} end
  l.spans = {{s = "[!] ", fg = 196}, unpack(l.spans)}   -- assign a new table (in-place edits are not seen)
end)
```

Return `{gag=true}` to hide the line, `{spans=...}` to replace it, `{stop=true}` to stop later rules.
A gag does **not** stop later rules (so a gagged line can still trigger an auto-heal).

**Ordering:** triggers and `line` handlers form one list, run by `priority` (higher first, default 0), then in
registration order. This is deterministic and identical on live and replayed lines.

`squat.on` returns an id; `squat.remove(id)` (alias `squat.cancel`) unregisters it.

## 5. Triggers and aliases

```lua
squat.trigger{
  match    = "You are hit by * for * damage.",  -- glob unless regex=true
  regex    = false,        -- true: RE2 regular expression (unanchored search)
  icase    = false,        -- case-insensitive
  gag      = false,        -- hide the line (display effect, also on replay)
  fg = "#ff8800", bg = 17, b = true, u = true,   -- highlight (display effect, also on replay)
  hilite   = "line",       -- "line" (default) or "match": colour only the matched text
  world    = "MyMUSH",     -- optional: only this world (name or id)
  priority = 0,
  fn       = function(m) ... end,   -- optional, live lines only
}
```

**Glob:** matches the whole line; `*` matches any run (captured), `?` one character, `\*` and `\?` are literal.
**Regex:** RE2 syntax (<https://github.com/google/re2/wiki/Syntax>): linear-time, no backreferences or
lookaround; named groups `(?P<name>...)`.

`fn(m)` gets `m.text`, `m[0]` (whole match), `m[1..n]` (captures; `""` if a group did not take part),
`m.named.<name>`, `m.world`, `m.live`, `m.spans`, `m.prompt`. It may return `{gag=true}`/`{spans=...}`/`{stop=true}`.

Aliases run on text the user typed:

```lua
squat.alias{match = "k *", fn = function(m) squat.send("kill " .. m[1]) end}
```

Same match options (`regex`, `icase`, `world`, `priority`). An alias handles the input (it is not sent) unless
its function returns `false`; the first handling alias wins.

## 6. Timers

```lua
squat.timer{every = 30, count = 5, fn = function() ... end}   -- count omitted: repeat forever
squat.after(2.5, function() ... end)                          -- once
local id = squat.timer{...};  squat.cancel(id)
```

Intervals are seconds (minimum 0.05 by default, at most 512 live timers). Timers are removed on plugin reload. A tick that errors is logged and the timer keeps going.

## 7. Variables

`squat.var.get(name [, default])`, `squat.var.set(name, value)`, `squat.var.del(name)`, `squat.var.keys()`.
Values are strings, numbers, booleans, or tables of those (JSON-compatible; functions and cycles are rejected).
`set(name, nil)` deletes. The store is shared by all plugins (prefix your names) and survives
`/reload`.

## 7b. Windows and panels: `squat.ui`

A script can open its own windows and docked panels in the SquatClient window. The runtime only keeps them as
data (the variable `ui.windows`); the client draws them, so they cost nothing in a client that has no window.
They belong to the script that opened them and disappear when it is unloaded (for a layout, when another
layout is chosen).

```lua
squat.ui.window{id = "who", title = "Who is online", x = 140, y = 90, w = 420,
  body = {type = "column", gap = 8, children = {
    {type = "table", columns = {{key = "name", title = "Name"}}, rows = {{name = "ann"}}},
    {type = "button", text = "Refresh", send = "/who"},
  }}}
squat.ui.panel{id = "trail", title = "Trail", dock = "right", body = {type = "text", value = "Limbo"}}
squat.ui.update("who", {title = "Who (3)"})     -- shallow merge into the spec
squat.ui.close("who");  squat.ui.list();  squat.ui.get("who")
```

`window` floats (the user drags it by the title bar and closes it with the ×; `open` is the same); `panel` docks
where a layout puts `<LuaPanels dock="right" />`. At most 32 at a time.

A `body` is a tree of nodes `{type = ..., children = {...}, ...props}`:
`column`, `row`, `grid` (`columns`), `panel` (`title`, `collapsible`, `open`), `tabs` (children with `title`),
`separator`, `heading` (`value`), `text` (`value`, `dim`, `bold`, `mono`, or `bind = "gmcp:Char.Vitals.hp"`),
`code` (`value`), `badge` (`value`, `tone` = ok|warn|danger), `button` (`text`, `send`, `tip`, `disabled`),
`table` (`columns`, `rows`, `empty`), `list` (`items`, `text`, `send` with `{name}`, `title`),
`gauge` (`value`, `max`, `label`, `color`), `input` (`placeholder`, `send` with `{value}`; Enter sends).
Unknown types draw nothing. Nothing in a spec is code: a button's `send` is text that goes through the normal
input pipeline, so a Lua alias can answer it (`send = "/who"` + `squat.alias{match = "/who", ...}`).

## 8. Regular expressions

`squat.regex.match(pattern, text)` returns `nil` or a table `m[0]`, `m[1..n]`, `m.named`;
`squat.regex.test(pattern, text)` returns a boolean; `squat.regex.replace(pattern, text, repl)` uses `$1`/`${name}`.

## 9. Talking to the TinyFugue-style language

`squat.tf(code [, world])` runs one line of TinyFugue-style input (`true` or `nil, err`).
Going the other way, the `/lua <code>` command runs Lua in a persistent console environment that keeps its
globals between commands.

## 10. Plugins

```
plugins/
  heal.lua              a single-file plugin named "heal"
  combat/
    plugin.json         {"name": "combat", "version": "1.0", "description": "...", "enabled": true, "main": "main.lua"}
    main.lua            entry point (default main.lua, then init.lua)
    util.lua            require("util")
```

`plugin.json` is optional; `"enabled": false` skips the plugin. A plugin that fails to load is reported and skipped.
`/reload` discards all handlers, timers and plugin globals and re-runs every plugin from disk.

## 11. Examples


Auto-heal:

```lua
squat.trigger{
  match = "^HP: (\\d+)/(\\d+)", regex = true,
  fn = function(m)
    local hp, max = tonumber(m[1]), tonumber(m[2])
    if hp / max < 0.4 and squat.now() - squat.var.get("last_heal", 0) > 3 then
      squat.var.set("last_heal", squat.now())
      squat.send("cast heal")
    end
  end,
}
```

Truecolor highlight:

```lua
squat.trigger{match = "*tells you*", fg = "#ff8800", b = true}
squat.trigger{match = "treasure", regex = true, hilite = "match", fg = "#ffd700", bg = 52}
```

Gag spam (still counting it on live lines):

```lua
squat.trigger{match = "^<Spam> ", regex = true, gag = true,
  fn = function() squat.var.set("spam_count", squat.var.get("spam_count", 0) + 1) end}
```

Timer (anti-idle, per connection):

```lua
squat.on("connect", function() squat.timer{every = 300, fn = function() squat.send("") end} end)
```

Alias:

```lua
squat.alias{match = "k *", fn = function(m) squat.send("kill " .. m[1]) end}
```

Call the TinyFugue-style language:

```lua
squat.trigger{match = "* arrives.", fn = function(m) squat.tf("/echo -aB Welcome, " .. m[1] .. "!") end}
```

## 12. Limits (what the budget can and cannot stop)

Enforced per call: VM instruction count (main thread), wall-clock timeout (also stops coroutines), heap-growth
ceiling (sampled every 65536 instructions, process-wide so approximate), Lua call depth and stack size,
`string.rep` size, timer/variable counts. **Not** interruptible: a single long-running built-in such as a
pathological `string.find` pattern or very wide `string.format`; the call is marked failed when it returns, but it
can still occupy the (single) script thread until then. Prefer `squat.regex` (RE2, linear time) for untrusted patterns.
