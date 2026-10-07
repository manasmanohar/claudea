# claudea

**Switch Claude Code between accounts in one keystroke — and see every account's usage before you pick.**

```
 Claude accounts

╭──────────────────────────────────────────────────╮
│ 1  work ✓                                        │
│    work@example.com                     just now │
│    5h  ━━━──────────────────────   11%  11:19pm  │
│    7d  ━━━━━━━━━━━━━━━━━━───────   72%  sat 4am  │
╰──────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────╮
│ 2  personal                                      │
│    me@example.com                         2m ago │
│    5h  ━━━━━━━━━━━━━━━━━────────   67%  12:19am  │
│    7d  ━━━━━━━━━━━━─────────────   49%  thu 3am  │
╰──────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────╮
│ 3  side-project                                  │
│    builds@example.com                   just now │
│    5h  ─────────────────────────    0%  2:29am   │
│    7d  ━━━━─────────────────────   14%  sat 10am │
╰──────────────────────────────────────────────────╯
   + add account

 ↑↓ move · enter switch · 1-9 jump · d remove · a add · r refresh · q quit
```

> Unofficial personal tool for macOS. Not affiliated with Anthropic. It uses Claude Code's
> own login storage and an undocumented usage endpoint, either of which can change without notice.

---

## Why

If you use more than one Claude account (work, personal, a second seat for heavy weeks),
switching means `/login`, a browser round-trip, and signing in again — every time. And you
can't see which account still has room in its 5-hour or weekly limit without logging into each.

`claudea` saves each login once. After that, switching is **↑↓ + Enter**, and every account's
limits are on one screen, so you switch to the one that has headroom.

## What it does

- **One-keystroke switching** for the whole machine — every new `claude` session uses the chosen account.
- **Usage for every account at a glance** — 5-hour and weekly limits as bars, with reset times.
- **Guided onboarding** — `+ add account` opens the normal browser sign-in and saves the result.
- **Detects an unsaved login** — if you `/login` inside Claude to a new account, it shows up as
  `● … not saved` and one Enter saves it.
- **Polite to the server** — one usage check at a time, at most once per account per 30 seconds,
  and a 5-minute back-off after any rate-limit response (details below).
- **Never loses a login** — before switching away, it re-saves the current account's latest tokens.

## Quick start

**Requirements:** macOS, [Claude Code](https://claude.com/claude-code) installed and signed in, Go 1.23+ to build.

```sh
git clone https://github.com/manasmanohar/claudea ~/claudea
cd ~/claudea && go build -o ~/.local/bin/claudea .     # any directory on your PATH works
```

**Save your accounts (once per account):**

```sh
claudea
```

1. The account you're signed into appears as `● you@example.com  not saved` → press **Enter**, name it (e.g. `work`).
2. Move to **`+ add account`** → **Enter**. Your browser opens the normal Claude sign-in; sign in with the next account.
3. Back in `claudea`, name it. Repeat for each account.

**Daily use:** run `claudea`, pick an account, press **Enter**. New `claude` sessions now use it.

## Using it

### The screen

```
╭──────────────────────────────────────────────────╮
│ 1  work ✓                                        │  ← number = order you added it; ✓ = the account Claude Code uses now
│    work@example.com                     just now │  ← when this card's numbers were last checked
│    5h  ━━━──────────────────────   11%  11:19pm  │  ← 5-hour limit: % used, when it resets
│    7d  ━━━━━━━━━━━━━━━━━━───────   72%  sat 4am  │  ← weekly limit
╰──────────────────────────────────────────────────╯
```

Cards keep their position and number; switching only moves the ✓. The cursor is the card with the
highlighted border and bold name. Bars fade from green to red as usage grows. Card status (right of
the email) is one of: `⠦ updating`, `queued`, `2m ago`, `offline`, `update failed`, `rate limited · retry in 4m`. When an update fails, the reason is
shown in red as the card's last line.

### Keys

| Key | Action |
|---|---|
| `↑` `↓` / `k` `j` | Move between accounts |
| `1`–`9` | Jump to that account's card (Enter still switches) |
| `Enter` | Switch to the selected account (or save the `not saved` login, or start `+ add account`) |
| `a` | Add an account (opens the browser sign-in) |
| `d` | Remove the selected account from `claudea` (asks `y / n`; does not sign out anywhere) |
| `r` | Re-check usage for accounts not checked in the last 30 seconds |
| `q` / `Ctrl+C` | Quit |

### Commands

```sh
claudea              # the account switcher
claudea ls           # list saved accounts, ✓ marks the active one
claudea use work     # switch without opening the switcher
claudea -h           # help
```

Example:

```
$ claudea ls
1   work           work@example.com ✓
2   personal       me@example.com
3   side-project   builds@example.com

$ claudea use personal
switched to 'personal'
```

## How it works

Claude Code keeps its login on macOS in exactly two places. Switching accounts means
rewriting both:

```
~/.claude.json   →  "oauthAccount": { email, org, plan … }    who you are (shown in the UI)
macOS Keychain   →  "Claude Code-credentials"                  the secret tokens
```

`claudea` keeps one saved copy of each per account, and a switch swaps them in:

```
                 claudea use personal
                        │
   1. re-save live tokens of the current account (tokens rotate; an old copy may be dead)
                        │
   2. Keychain "claude-acct / personal"  ──copy──▶  Keychain "Claude Code-credentials"
                        │
   3. ~/.claude-accounts/personal.json   ──copy──▶  ~/.claude.json → "oauthAccount"
                                                    (previous file backed up first)
```

Every **new** `claude` session uses the switched account. Sessions already running keep working
and do not overwrite the switch when they next save `~/.claude.json` (verified). Whether an
already-running session starts spending the new account's quota before it restarts is not verified.

### Usage checks and rate limits

Usage comes from the same endpoint Claude Code's `/usage` screen uses. That endpoint
rate-limits repeat checks of one account (observed: about 3 checks within 30 seconds returns
HTTP 429, clearing within ~10 minutes), and `claudea` shares that budget with Claude's own `/usage`.
So `claudea`:

- shows the **last known numbers instantly** from a local cache,
- checks **one account at a time**, 2 seconds apart,
- checks each account **at most once per 30 seconds** — `r` inside that window just tells you
  `next check possible in 19s`,
- after a 429, keeps the old numbers, shows `rate limited · retry in 4m`, and waits **5 minutes**
  before trying that account again — remembered on disk, so reopening can't bypass it,
- never polls on a timer — the only automatic re-check is a single retry after a rate-limit wait,
  and only while the screen is open.

Checking an account you're not using may renew its tokens; switching to or removing an account
is blocked for the moment its check is running, so a renewal can't be interrupted.

## What it touches

| Location | Contents | Secret? |
|---|---|---|
| Keychain, service `claude-acct`, one item per account | That account's Claude Code tokens | **Yes** — stays in the Keychain |
| Keychain, `Claude Code-credentials` | Claude Code's live login (rewritten on switch) | **Yes** |
| `~/.claude.json` → `oauthAccount` | Live account profile (rewritten on switch) | No |
| `~/.claude-accounts/<name>.json` | Saved account profile (email, org, plan) | No |
| `~/.claude-accounts/usage.cache` | Last usage numbers and check times | No |
| `~/.claude-accounts/claude.json.bak` | `~/.claude.json` as it was before the last switch | No |

Network calls: Anthropic's usage endpoint (usage numbers) and token endpoint (renewing a saved
account's tokens). Nothing else, no telemetry.

**Roll back a switch by hand:** `cp ~/.claude-accounts/claude.json.bak ~/.claude.json`, then
`claudea use <previous account>`.

## Troubleshooting

| You see | Meaning / fix |
|---|---|
| `update failed` + a red reason line | The reason is Anthropic's or claudea's own message, e.g. `OAuth authentication is currently not allowed for this organization` (403) means that account's organisation blocks OAuth sign-in; claudea can't fix it. The bars shown are the last known ones. |
| `rate limited · retry in 4m` | The usage endpoint throttled that account. Wait; the numbers shown are the last known ones. Avoid pressing `r` in Claude's `/usage` meanwhile. |
| `offline` / `can't reach Anthropic — check your connection` | No network. The numbers shown are the last known ones. |
| `sign-in expired — open claude once to renew it` | The active account's sign-in lapsed. Start `claude` once; it renews it. |
| `sign-in expired — press a and sign in to this account again` | A saved account's sign-in is no longer valid. Press `a`, sign in to it; it's recognised and updated. |
| `save the signed-in account first — press Enter on its card at the top` | You're signed into an unsaved account. Save it before adding or switching. |
| `you signed in elsewhere meanwhile — screen refreshed, press a again` | You ran `/login` in Claude while the switcher was open; nothing was overwritten. |
| `updating this account — try again in a moment` | Its usage check is in progress; retry in a second. |
| An old session still shows the previous account | New sessions use the switched account; restart long-running sessions if you need them to. |

## Limitations

- macOS only (the login lives in the macOS Keychain).
- One global login at a time, by design — the whole machine switches together.
- Relies on Claude Code internals (`~/.claude.json` layout, Keychain item name) and undocumented
  endpoints. A Claude Code update can break it; the switch itself is plain file/Keychain copies.

## Uninstall

```sh
rm ~/.local/bin/claudea
for n in $(ls ~/.claude-accounts/*.json | xargs -n1 basename | sed 's/\.json$//'); do
  security delete-generic-password -s claude-acct -a "$n"
done
rm -rf ~/.claude-accounts
```

Your current Claude Code login is left as it is.

## Project layout

```
main.go    command-line entry: claudea / claudea ls / claudea use <name>
store.go   Keychain + file storage, switching, token renewal, usage requests, cache
ui.go      the interactive screen (Bubble Tea + Lip Gloss + Bubbles)
```

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lip Gloss](https://github.com/charmbracelet/lipgloss) and
[Bubbles](https://github.com/charmbracelet/bubbles).
