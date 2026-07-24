# Security Design

> Design decisions and mechanisms that protect against accidental data loss, binary misidentification, and shell injection.

## Contents

- [1. Agent Identity Verification](#1-agent-identity-verification)
- [2. Shell Command Safety](#2-shell-command-safety)
- [3. Protect Patterns](#3-protect-patterns)
- [4. Empty Source Guard](#4-empty-source-guard)
- [5. Host Key Verification](#5-host-key-verification)
- [6. Remote Command Execution](#6-remote-command-execution)

## 1. Agent Identity Verification

Shuttle uses a unique identifier mechanism to confirm a remote binary is actually the Shuttle agent — not an unrelated program with the same name.

**Problem:** Multiple projects ship binaries named `shuttle`. The old approach of running `shuttle version` and checking for output starting with "Shuttle" was too weak — any program could produce that.

**Solution:** A hidden `identify` subcommand outputs a deliberately unique string:

```
SHuTtL3_AgEnT_lD:0.1.5.12:linux/amd64:md5,sha256,xxh64,xxh3
```

The mixed-case prefix `SHuTtL3_AgEnT_lD:` is not producible by any other software. All agent operations verify this prefix before trusting the binary:

| Operation | Verification |
|-----------|-------------|
| `deploy` | Post-upload identify check; removes binary on failure |
| `agent status` | Searches paths, tests each with identify |
| `agent remove` | Only deletes after identify verification |
| `push` | Checks agent before delta transfer; falls back to SFTP if absent |

**Display:** The identify output is machine-only. User-facing commands show the `version` output instead.

Related: [Remote Agent](agent.md)

## 2. Shell Command Safety

All paths embedded in remote shell commands are escaped to prevent injection.

### shellPath() function

```go
func shellPath(p string) string {
    if strings.Contains(p, "$") {
        return p  // needs shell expansion, assume safe
    }
    return "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
}
```

| Path pattern | shellPath output | Why |
|-------------|-----------------|-----|
| `/usr/local/bin/shuttle` | `'/usr/local/bin/shuttle'` | Literal: single-quoted |
| `$HOME/shuttle` | `$HOME/shuttle` | Variable: passed through for shell expansion |

### Delta command

The `shuttle receive` command uses single-quote escaping for both the algorithm and path:

```go
fmt.Sprintf("shuttle receive --algo '%s' '%s'", algo, escapedPath)
```

Inside single quotes, no shell metacharacters (`$`, `` ` ``, `(`, `)`, `;`, `|`) are interpreted. Only the single quote itself needs escaping via `'\''`.

## 3. Protect Patterns

Each server can define glob patterns that prevent remote files from being overwritten or deleted:

```yaml
servers:
  - name: myserver
    protect:
      - "*.db"        # all database files
      - "*.pem"       # private keys
      - "config.yaml" # specific config
```

Protected files are:
- Never overwritten during sync
- Never deleted during cleanup
- Listed as `PROT` in dry-run output

## 4. Empty Source Guard

If the source directory contains no files and `delete: true` is set, shuttle refuses to sync:

```
safety: source contains no files and delete is enabled
```

This prevents accidental remote wipes when the source is empty (e.g., misconfigured path, or `show_dots: false` hiding all files).

## 5. Host Key Verification

SSH host keys are verified against `~/.ssh/known_hosts`:

- Unknown hosts: key is automatically added (trust-on-first-use)
- Changed keys: connection is rejected
- Missing known_hosts file: created automatically

## 6. Remote Command Execution

The `SFTPTransport.Exec` method runs commands on the remote server via SSH. It is **not exposed to users** — only internal sync code calls it with hardcoded command templates. The API carries a warning:

```go
// WARNING: this method executes arbitrary commands over SSH. Only call with
// hardcoded or strictly validated command strings — never with user input.
```

Shuttle does not provide a "run arbitrary command on remote" feature. Use `ssh` directly for that.
