# Che

Che installs tools from release assets and keeps them in line with a config file.

- [config.md](config.md): the sources file.
- [commands.md](commands.md): what each command does.
- [plan-apply.md](plan-apply.md): how changes are planned and applied safely.

## Files

Three files, each written by one command:

| File | Holds | Written by |
| --- | --- | --- |
| `sources.yaml` | what you want installed | you, `add`, `rm` |
| `sources.lock` | the version of each source, and asset checksums | `update`, `apply` |
| `sources.state.yaml` | what's installed: versions, install dirs, links | `apply` |

They live in `$XDG_CONFIG_HOME/che/` (or `~/.config/che/`). `-sources <path>` points at another sources file; the lock and state sit next to it with the same base name.

## Workflow

```sh
che add tools.rg github:BurntSushi/ripgrep -asset 'ripgrep-${version}-x86_64-unknown-linux-musl.tar.gz' -bin 'rg=*/rg'
che plan       # what apply would do
che apply      # do it
che outdated   # what's newer upstream
che update     # lock the newer versions
che apply      # install them
che rm rg      # drop from the config
che apply      # uninstall
```

`update` and `apply` are separate on purpose: commit the lock, and every machine running `apply` gets the same versions.

## Identity

A source's ID is its dotted path in the config, e.g. `tools.rg`. Moving or renaming a source changes its ID, which plans as removing the old ID and installing the new one.

An ID that's installed but no longer configured is an orphan. `apply` uninstalls it; adding it back to the config cancels that.

## Supported

| | Status |
| --- | --- |
| GitHub releases | yes |
| URLs listing releases, with jq | yes |
| `tar.gz`, `tar.xz`, `zip`, `gz`, `xz`, plain binaries | yes |
| GitLab, git sources and `build` | not yet |
| `deb` and `rpm` packages | not yet |
| `command` installs | not yet |
