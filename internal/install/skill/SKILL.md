---
name: sonar
description: "Managing localhost ports and dev servers: starting services, freeing ports, picking free ports, cleaning up. Use whenever a task starts a server, hits 'address already in use', or needs to know what is running."
---
<!-- managed by sonar; version 1 -->

# Sonar

## Overview

Sonar owns localhost. Every long-lived process — dev servers, APIs, databases,
workers — goes through `sonar start` so it is attributed to a project, a git
worktree and this agent session, its logs are captured, and it can be found and
stopped later. Backgrounding a server yourself makes it invisible and orphaned.

## When to use

- A task needs a dev server, API, database, or any process that keeps running.
- A command failed with `address already in use`, `EADDRINUSE`, or
  `port is already allocated`.
- You need a port for a new service and do not know which are free.
- The task is finished and something you started is still listening.

## Session attribution

Sonar attributes processes with `SONAR_SESSION` (`<tool>:<id>`). If the Claude
Code hooks are installed it is already set. If it is unset and
`CLAUDE_SESSION_ID` is available, set it once before starting anything:

```bash
export SONAR_SESSION=claude-code:$CLAUDE_SESSION_ID
```

## The rules

1. **Start servers through sonar.** Run dev servers, APIs and databases as
   `sonar start -- <command>`. Sonar infers the project from the git root,
   attributes the process to this session, captures logs, and makes cleanup
   possible later. Never background a server with `&` or `nohup`.
2. **Wait before testing.** After starting, `sonar wait <port> --timeout 60s`
   (or `--http /health`) before curl, tests, or browser checks. Do not sleep.
3. **Pick ports with sonar.** For a new server, `sonar claim` returns ports
   reserved for this project and worktree, so parallel agents in other
   worktrees do not collide. Use `sonar next` only for throwaway ports.
4. **"Address already in use" playbook.** `sonar info <port>` first. If it is
   this session's own earlier run, `sonar kill --session $SONAR_SESSION` or
   `sonar kill <port>`. If it belongs to another session, worktree, or a human,
   do not kill it — claim a different port and tell the user. If it is a Docker
   container, say so; `sonar kill` stops containers safely.
5. **Clean up.** When the task ends, `sonar kill -g <group>` for what this
   session started, or `sonar kill --session $SONAR_SESSION`. Prefer
   `--dry-run` when unsure.
6. **Look before killing.** `sonar list` shows groups, sessions, and who
   started what. Never `kill -9` a PID found by grepping `lsof`.

## Quick reference

| Goal | Command |
|---|---|
| Start a server | `sonar start -- npm run dev` |
| Start it in a group | `sonar start -g web -- npm run dev` |
| Wait for it | `sonar wait 3000 --timeout 60s` |
| Wait for a health path | `sonar wait 3000 --http /health` |
| See everything listening | `sonar list` |
| Who owns a port | `sonar info 3000` |
| Reserve ports for this worktree | `sonar claim` |
| One throwaway free port | `sonar next` |
| Read a server's output | `sonar logs 3000` |
| Stop one port | `sonar kill 3000` |
| Stop this session's servers | `sonar kill --session $SONAR_SESSION` |
| Preview a kill | `sonar kill 3000 --dry-run` |

## Common mistakes

- `npm run dev &` — the process is orphaned, its logs are lost, and nothing can
  clean it up. Use `sonar start -- npm run dev`.
- `sleep 5 && curl localhost:3000` — flaky. Use `sonar wait 3000`.
- `lsof -ti:3000 | xargs kill -9` — kills whatever happens to be there,
  including a colleague's or another agent's server. Use `sonar info 3000`,
  then `sonar kill 3000`.
- Hard-coding port 3000 in a worktree while three other agents want it. Use
  `sonar claim`.
