# Commands

```
che <command> [args] [flags]
```

Flags can go before, after or between arguments; everything after `--` is an argument. `-sources <path>` works with every command.

## Targets

Commands taking IDs accept, in order of preference:

1. an exact configured ID,
2. an exact installed ID, which is how orphans are named,
3. the last segment of a configured ID, if only one matches: `rg` for `tools.rg`.

Unknown and ambiguous arguments are errors. Without IDs, `plan`, `apply`, `update` and `outdated` take every source.

## add

```
che add <id> <origin> [flags]
```

Adds a source to the sources file, creating the file and groups as needed. Nothing else is written.

| Origin | Source |
| --- | --- |
| `github:owner/repo`, `owner/repo`, `https://github.com/owner/repo` | GitHub |
| `gitlab:group/repo`, `https://gitlab.com/group/repo` | GitLab |
| `git:<url>` | git |
| any other `http(s)` URL | URL source |

Flags set the other fields: `-asset`, `-format`, `-version`, `-ref`, `-build`, `-command`, `-install-dir`, `-bin-dir`, `-jq`, and the repeatable `-bin name=path` (or `-bin path`), `-rename key=value`, `-env KEY=value`.

Before writing, the new source is planned against upstream, so a source whose release or asset can't be found isn't added. `-no-check` skips that, e.g. offline.

The entry is inserted at the end of the deepest existing group of its ID. Only lines are added: the rest of the file, comments and formatting included, is untouched. Values are double-quoted, indentation follows the file.

## rm

```
che rm <id>... [-yes]
```

Removes sources from the sources file, after confirmation. Comment lines right above a source go with it, and groups left empty are removed. Installed sources are uninstalled by the next `apply`.

## plan

```
che plan [id...]
```

Prints what `apply` would do. Nothing is written. Configured sources without a lock entry are locked in memory first, and listed as `will lock`. See [plan-apply.md](plan-apply.md).

## apply

```
che apply [id...] [-yes]
```

Plans, asks for confirmation, then makes the machine match the config and lock. Writes the lock and state. Refuses when any source failed to plan or any action is blocked. Lock entries of sources no longer configured are dropped.

## update

```
che update [id...]
```

Locks the latest version allowed by each source's `version`. Only writes the lock; run `apply` to install. Orphans can't be targeted.

## outdated

```
che outdated [id...]
```

```
ID              INSTALLED   LOCKED     WANTED     LATEST
runtimes.node   v22.21.0    v22.21.0   v22.23.5   v26.13.1   constraint 22.x
tools.fzf       -           v0.75.0    v0.75.0    v0.75.0
```

| Column | Meaning |
| --- | --- |
| INSTALLED | the state, `-` if not installed |
| LOCKED | what `apply` would install |
| WANTED | what `update` would lock |
| LATEST | the latest release, ignoring `version` |

Sources whose columns all agree are left out. Nothing is written.
