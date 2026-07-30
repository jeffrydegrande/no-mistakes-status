# nms

A dashboard for every [no-mistakes](https://github.com/kunchenguid/no-mistakes) pipeline run on this machine.

no-mistakes is repo-scoped: `no-mistakes status` and `no-mistakes runs` only show the repository you are standing in. One daemon drives every repo, so this reads its state database directly and shows all of them in one table.

```
  no-mistakes  ·  6 active  ·  1 parked  ·  3 recent

  REPO           BRANCH                                       STAGES      STEP        AGE     PR
  active
▸ nova-go        ngo-165-rls-spike                            ●●◍○○○○○○○  review !    9m      -
  nova-go        ngo-167-bound-provider-and-turn-output       ●●◐○○○○○○○  review      10s     -
  nova-go        fix-gateway-message-count-auth               ●●●●●●●●●◐  ci          31m     #192 open
  portal-server  seq-15-bayard-lane-separation                ●●●●●●●⊘●◐  ci          3h52m   #5187 open

  recent
  portal-server  seq-11-12-final                              ●●●●●●●⊘●⊘  done        1h3m    #5189 open
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
```

| Key | Does |
| --- | --- |
| `↑` `↓` / `k` `j` | move between runs |
| `enter` | open the selected run's detail |
| `o` | open the run's pull request in a browser |
| `s` | cycle sort: urgency, age, repo |
| `r` | refresh now |
| `?` | help and the stage legend |
| `q` | quit |

The detail page shows the full branch, head SHA, every stage with its duration and findings, the agent's last reported activity, the PR link, and the run's recorded intent.

## Stages

Each run renders as one strip, one slot per pipeline stage, in pipeline order.

| Glyph | Meaning |
| --- | --- |
| `●` | done |
| `◐` | running (animated) |
| `◑` | fixing |
| `◍` | parked, waiting on a decision |
| `✗` | failed |
| `⊘` | skipped |
| `○` | not started |

## How it reads the data

It opens `~/.no-mistakes/state.sqlite` (or `$NM_HOME/state.sqlite`) with `PRAGMA query_only`, so it can never write to a live pipeline's state. The daemon keeps the database in WAL mode, so polling never blocks a running pipeline and a concurrent write never blocks a poll.

That database belongs to no-mistakes and its schema can change. On startup `nms` probes for every column it depends on and reports exactly what is missing rather than failing later with an opaque scan error.
