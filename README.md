# nms

A dashboard for every [no-mistakes](https://github.com/kunchenguid/no-mistakes) pipeline run on this machine.

no-mistakes is repo-scoped: `no-mistakes status` and `no-mistakes runs` only show the repository you are standing in. One daemon drives every repo, so this reads its state database directly and shows all of them in one table.

```
  no-mistakes  ·  7 active  ·  1 parked  ·  1 stuck  ·  4 recent

  REPO           BRANCH                                      STAGES      STEP         AGE    LEFT      TH  PR
  active
▸ nova-go        ngo-165-rls-spike                           ●●◍○○○○○○○  review !     41m    you       4   -
  nova-go        ngo-168-prove-the-process-memory-bound      ●●●◑○○○○○○  test fix     29m    ~49m      6   -
  nova-go        ngo-167-bound-provider-and-turn-output      ●●◐○○○○○○○  review ⚠     50s    stuck     5   -
  nova-go        fix-gateway-message-count-auth              ●●●●●●●●●◐  ci           1h3m   any time      #192 open
  portal-server  seq-15-bayard-lane-separation               ●●●●●●●⊘●◐  ci           4h24m  -             #5187 open

  recent
  portal-server  seq-11-12-final                             ●●●●●●●⊘●⊘  done         1h35m  -             #5189 open
```

## Install

```sh
go install github.com/jeffrydegrande/no-mistakes-status/cmd/nms@latest
```

## Use

```sh
nms                 # live dashboard, refreshes every 5s
nms --once          # print the table once and exit (pipe-friendly)
nms --interval 2s   # poll faster
nms --recent 24h    # look further back for finished runs
nms --stall 15m     # be more patient before calling a step stuck
nms --notify=false  # no desktop notifications
```

| Key | Does |
| --- | --- |
| `↑` `↓` / `k` `j` | move between runs |
| `enter` | open the selected run's detail |
| `d` | read the run's log, formatted |
| `o` | open the run's pull request in a browser |
| `p` | show only runs waiting on a decision |
| `f` | show only the selected run's repo |
| `s` | cycle sort: urgency, finishing next, age, repo |
| `r` | refresh now |
| `?` | help and the stage legend |
| `q` | quit |

## What it shows

**Stages.** Each run renders as one strip, one slot per pipeline stage, in pipeline order.

| Glyph | Meaning |
| --- | --- |
| `●` | done |
| `◐` | running (animated) |
| `◑` | fixing |
| `◍` | parked, waiting on a decision |
| `✗` | failed |
| `⊘` | skipped |
| `○` | not started |

**LEFT** is how much longer, estimated from this machine's own finished runs: what the current step still owes against its usual duration, plus the usual cost of every step after it. A step with no history contributes nothing, so the number is a floor, not a guess, and a run with no history at all shows `-` rather than a made-up figure. Baselines are medians per repository and step, so one CI run that sat six hours waiting on a merge does not poison the next twenty estimates. A parked run shows `you`, because it is not waiting on time.

**Stuck runs.** A pipeline step runs an agent in a worktree with no terminal attached. When that agent stops to ask for permission, nobody is there to answer: the step stays `running`, the process stays alive, and nothing is ever written again. Two shapes of that are detectable and both get a `⚠` and a notification — a running step that has reported nothing for `--stall` (8 minutes by default), and a step whose recorded agent process no longer exists.

**Notifications.** A desktop notification when a run parks, fails, or gets stuck. Only on the transition, so a run that stays parked for an hour interrupts you once. Uses `notify-send` on Linux and `osascript` on macOS, and turns itself off when neither is available.

**Detail** (`enter`) shows the full branch, the working directory, head SHA, every stage with its duration and typical duration, the findings the pipeline actually raised (the ones needing a human first), the agent's last reported activity, the PR as a clickable link, a tail of the log, and the run's recorded intent.

**Logs** (`d`) are reformatted rather than dumped. Agents write structured payloads into the same stream as plain progress lines, both pretty-printed and several concatenated onto one line; those are taken apart and printed as labelled prose, summary first, nulls and empty fields hidden.

**Treehouse.** When [treehouse](https://github.com/jeffrydegrande/treehouse) is installed, a `TH` column shows which pooled worktree is driving each run. A run does not record its worktree, so this matches the lease holder's task key against the branch name; unmatched runs simply stay blank.

## How it reads the data

It opens `~/.no-mistakes/state.sqlite` (or `$NM_HOME/state.sqlite`) with `PRAGMA query_only`, so it can never write to a live pipeline's state. The daemon keeps the database in WAL mode, so polling never blocks a running pipeline and a concurrent write never blocks a poll.

That database belongs to no-mistakes and its schema can change. On startup `nms` probes for every column it depends on and reports exactly what is missing rather than failing later with an opaque scan error.

## License

MIT
