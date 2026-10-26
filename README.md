# gh-inbox

A [GitHub CLI](https://cli.github.com) extension that cleans up your
notifications inbox: mark notifications as done or read, or unsubscribe from
them, in bulk, by repository, type, reason, age, state, or the people
involved.

```console
$ gh inbox clean --state merged,closed
Would mark 1835 notifications as done.

1835 notifications

By repository
  321  acme/gitops
  132  acme/agent
  …

Run again with --apply to do it, or use "gh inbox list" with the same filters to see each notification.

$ gh inbox clean --state merged,closed --apply
Marked 1835 notifications as done.
```

## Install

```sh
gh extension install maraino/gh-inbox
```

Or, with Go 1.26.7 or later:

```sh
go install github.com/maraino/gh-inbox@latest
```

`go install` puts a `gh-inbox` binary in your `GOBIN`. Run it directly as
`gh-inbox`, or link the extension so `gh inbox` works too:

```sh
git clone https://github.com/maraino/gh-inbox && cd gh-inbox && gh extension install .
```

gh-inbox uses your `gh` login. Your token needs the `notifications` or `repo`
scope, and `read:org` for filters on review requests that name a team or
`@me`.

### GitHub Enterprise Server

Log in to the host with `gh`, then pass it with `--hostname`, or set
`GH_HOST`:

```sh
gh auth login --hostname github.example.com
gh inbox --hostname github.example.com list --summary
```

`gh` can also take the token from `GH_ENTERPRISE_TOKEN`. The record of
notifications marked as done is kept per host.

## Usage

```sh
gh inbox list   [filters] [--summary | --json] [--limit n]
gh inbox clean  [filters] [--action done|read|unsubscribe] [--apply] [--all]
gh inbox triage [filters]
```

`list` shows the notifications that match the filters, most recently updated
first. `clean` acts on them: it only shows what it would do unless you pass
`--apply`, and it refuses to act on every notification unless you pass `--all`.
`triage` goes through them one at a time: press `o` to open one in the
browser, `d` to mark it as done, `r` to mark it as read, `u` to unsubscribe,
`s` to skip it, or `q` to quit.

### Filters

| Flag | Matches |
|---|---|
| `--repo` | Repositories, as `owner/name`, with `*` wildcards |
| `--type` | `pr`, `issue`, `release`, `ci`, `discussion`, `advisory`, `dependabot`, `commit` |
| `--reason` | Why GitHub notified you: `review_requested`, `mention`, `subscribed`, `ci_activity`, … |
| `--state` | `open`, `closed`, `merged` |
| `--unread`, `--read` | Read status |
| `--older-than` | No activity for a while: `36h`, `7d`, `2w` |
| `--newer-than` | Activity recently: `24h`, `7d`; with `--older-than`, a window |
| `--author`, `--not-author` | Who opened the pull request or issue |
| `--assigned-to`, `--not-assigned-to` | The assignees |
| `--review-requested`, `--no-review-requested` | Users or `org/team`s whose review is requested |
| `--involves`, `--not-involves` | Assignees or requested reviewers |

- A filter with several values, comma-separated or repeated, matches when any
  value matches. A negated filter matches when none does. A notification must
  match every filter.
- Filters on people take a login or `@me`. Filters on review requests also
  take `org/team`, and for them `@me` includes your teams.
- GitHub drops a review request once the review is submitted, so
  `--review-requested @me` matches the reviews you still owe, while
  `--reason review_requested` matches every notification sent because your
  review was requested.
- Filters on the state or people of a pull request or issue don't match other
  notifications, such as CI runs. Pull requests and issues that can't be read
  anymore are skipped and listed.

### Examples

```sh
# What's in the inbox, by repository, type, reason and state
gh inbox list --summary

# Pull requests waiting for your review, or your teams'
gh inbox list --state open --review-requested @me

# Go through the reviews you owe one at a time
gh inbox triage --state open --review-requested @me

# What happened in the last day
gh inbox list --newer-than 24h

# Mark merged and closed pull requests and issues as done
gh inbox clean --state merged,closed --apply

# Open pull requests that are neither assigned to you nor waiting for your review
gh inbox clean --type pr --state open --not-involves @me --apply

# Mark CI notifications older than a week as read
gh inbox clean --type ci --older-than 7d --action read --apply

# Unsubscribe from threads you only follow because you watch the repository
gh inbox clean --repo 'acme/*' --reason subscribed --action unsubscribe --apply
```

### Done notifications

Marking a notification as done removes it from the inbox. It stays under the
"Done" filter on github.com, and comes back when there's new activity.

The API keeps listing done notifications, and can't tell them apart from read
ones. So gh-inbox keeps a record of the notifications it marked as done, in
`~/.local/state/gh-inbox/<host>/done.json` (or under `$XDG_STATE_HOME`), and
skips them until they have new activity. Notifications marked as done
elsewhere, such as on github.com, aren't in the record, and `clean` marks them
as done again, which is harmless. `--no-cache` ignores the record.

### Other flags

| Flag | Meaning |
|---|---|
| `--hostname` | The GitHub host, for GitHub Enterprise Server. Defaults to the `gh` default host. |
| `--concurrency` | How many requests `clean` makes at the same time (default 4) |
| `--verbose` | Log API requests and retries to stderr |

Requests that hit a rate limit or a transient server error are retried.

## Development

```sh
make          # lint, test and build
make test     # tests, with the race detector; RACE= runs them without it
make lint     # golangci-lint
make fmt      # format the code
make govulncheck
make install  # build and install the working tree as the gh extension
```

golangci-lint and govulncheck are Go tool dependencies, so `go tool` runs the
versions pinned in `go.mod`.

The code is in `internal/`:

| Package | Contents |
|---|---|
| `cmd` | The commands and their flags |
| `github` | The GitHub API client |
| `inbox` | The notification model |
| `filter` | The filters |
| `action` | Applying an action to many notifications at once |
| `cache` | The record of notifications marked as done |
| `render` | Table, summary and JSON output |

## License

[MIT](LICENSE)
