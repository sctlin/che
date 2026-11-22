# Plan and apply

## Planning

Each source compares what the config and lock want with what the state says is installed, and gets at most one action:

| Symbol | Action | When |
| --- | --- | --- |
| `+` | install | configured, not installed |
| `^` | upgrade | the locked version changed |
| `%` | reinstall | the downloaded asset, `bin`, `build`, `command` or `env` changed |
| `~` | modify | `install_dir` or `bin_dir` changed, or `asset`, `format` or `rename` changed without changing the downloaded asset |
| `*` | replace | the origin or the format changed |
| `-` | remove | installed, not configured |

The strongest change wins, in the order modify, reinstall, upgrade, replace. The release is only looked up when the asset could differ from the installed one, so unchanged sources need no network.

An action is blocked when it creates a link another installed source owns, unless that source's action in the same plan gives the link up. `apply` refuses blocked plans.

## Applying

`apply` runs in three phases, so a failed download never touches what's installed.

1. **Prepare**, writing only to temp and staging dirs: download every asset, verify its checksum, unpack it into `<install_dir>.new-XXXX`, and resolve each `bin` to exactly one file, made executable if it isn't. Problems from all sources are reported together. The lock is saved once everything is prepared, with checksums seen for the first time.
2. **Commit**, in the order remove, replace, modify, reinstall, upgrade, install. The previous install is renamed aside to `<install_dir>.old-XXXX`, the staged one renamed into place, links swapped. The state is saved after each action.
3. **Cleanup**, once every commit succeeded: the set-aside dirs are deleted.

### Safety

- Archives can't write outside their staging dir: no `..`, absolute paths or writes through symlinks.
- A link is replaced by renaming a new symlink over it, so it always points at the old or the new target.
- Che never overwrites a path at a link location or an install dir it doesn't manage.
- A link is only deleted while it still points where the state says. Links repointed by hand are left, with a warning.
- When a link moves between sources, the new owner overwrites it and the old one leaves it, so it doesn't go missing in between.

### Failures

A failed commit stops `apply`. The state then matches what was committed, uncommitted staging dirs are deleted, and set-aside dirs are kept and listed for recovery. Re-running `plan` shows what's left.

Not handled yet:

- Commits aren't transactional. If two sources swap links and the second commit fails, links and state disagree. An undo log for the commit phase would fix it.
- Parent dirs emptied by a remove, e.g. a group's dir, are left behind.

## Checksums

GitHub publishes asset digests, which downloads are checked against. Otherwise, and for URL sources without `sha256`, the first download's checksum is recorded in the lock, and later downloads must match it.
