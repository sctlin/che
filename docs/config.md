# Sources file

```yaml
defaults:
  install_dir: "/home/me/.che/pkgs"   # default: ~/.che/pkgs
  bin_dir: "/home/me/.local/bin"         # default: $XDG_BIN_HOME or ~/.local/bin

sources:
  lsp:
    defaults:
      rename:
        amd64: "x86_64"
    zls:
      github: "zigtools/zls"
      asset: "zls-${arch}-${os}.tar.xz"
  tools:
    fzf:
      github: "junegunn/fzf"
      version: "0.70.x"
```

Paths must be absolute, `~` and variables aren't expanded.

## Structure

- `defaults` at the top level apply to every source.
- `sources` is the root group. Its name isn't part of the IDs: the sources above are `lsp.zls` and `tools.fzf`.
- A mapping with a fetcher key (`github`, `gitlab`, `git`, `url`) is a source, any other mapping is a group.
- Groups can have their own `defaults`, merged over the inherited ones.
- Keys can't contain `.`, and `defaults` can't be a group or source name.

## Defaults

| Field | Inherited as |
| --- | --- |
| `install_dir` | each group appends its key, each source appends its key: `<install_dir>/tools/fzf` |
| `bin_dir` | as is |
| `rename` | merged, the closest wins |
| `env` | merged, the closest wins |

Sources can set any of them too.

## Source fields

| Field | Meaning |
| --- | --- |
| `github`, `gitlab` | `owner/repo` |
| `git` | repository URL |
| `url` | URL listing releases, see [URL sources](#url-sources) |
| `jq` | jq filter applied to the `url` response |
| `ref` | git ref to track, git only |
| `version` | constraint, see [Versions](#versions) |
| `asset` | exact asset name, may use the template variables |
| `format` | `tar.gz`, `tar.xz`, `zip`, `gz`, `xz`, `deb`, `rpm` or `package`; detected from the asset name if unset |
| `rename` | renames `os` and `arch` values to what the assets use, e.g. `amd64: x86_64` |
| `bin` | binaries to link, see [Bin](#bin) |
| `build` | build command for git sources |
| `command` | custom install command |
| `env` | environment for `build` and `command` |
| `install_dir`, `bin_dir` | override the inherited ones |

Exactly one of `github`, `gitlab`, `git`, `url` is required. `asset`, `version` and `format` don't apply to git; `build` and `command` exclude each other.

## Template variables

`asset` and `bin` paths may use:

| Variable | Value |
| --- | --- |
| `${os}` | `linux`, `darwin`, … after `rename` |
| `${arch}` | `amd64`, `arm64`, … after `rename` |
| `${tag}` | the release tag, e.g. `v0.75.0` |
| `${version}` | the tag without a leading `v` |

## Asset selection

Without `asset`, the release's asset must be the only one whose name contains both `os` and `arch`. Checksums, signatures and the like are skipped, and so are packages unless `format` asks for one. When several match, or none, the error lists the candidates to pick from with `asset`.

## Bin

`bin` maps link names to paths inside the install dir:

```yaml
bin: "bin/zls"                 # links zls
bin:
  rg: "ripgrep-*/rg"           # globs must match exactly one file
  godot: "Godot_v${tag}_linux.${arch}"
```

Without `bin`, the source's key is linked to a file of the same name. Link names can't be paths, `bin_dir` sets the directory. Two sources can't share a link.

`gz`, `xz` and plain binary assets are a single file, saved at the path of the only `bin` entry, which can't be a glob.

## Versions

| `version` | Locks |
| --- | --- |
| unset | the latest release: GitHub's latest, or the first one a URL lists |
| `1.x`, `1.x.x` | the highest `1.*.*` |
| `1.2.x` | the highest `1.2.*` |
| anything else | that exact tag, a leading `v` is optional |

Ranges need semver-compatible tags. A tag is compatible if it is semver after tidying: the prefix before the first digit is dropped (`jq-1.7.1`), and a `-stable`, `-release` or `-final` suffix (`4.7.2-stable`); missing minor and patch numbers are 0 (`0.14`). Anything else after a `-` is a prerelease, which ranges skip. Tags that aren't compatible are skipped too; pinning them exactly still works.

When a constraint changes, the lock entry is re-locked: to the installed version if it fits, otherwise to the highest that does.

## URL sources

`url` must return, after the optional `jq` filter, releases newest first:

```json
[
  {"tag": "v22.2.0", "assets": [{"name": "node-v22.2.0-linux-x64.tar.xz", "url": "https://…", "sha256": "…"}]},
  {"tag": "v22.1.0", "assets": []}
]
```

`sha256` is optional. A single release object, or a filter emitting several objects, works too. Unknown fields are errors, which catches typos like `tag_name`. Ordering, and filtering out prereleases, is the filter's job:

```yaml
node:
  url: "https://nodejs.org/dist/index.json"
  jq: '[[.[] | select(.lts)][] | .version as $v | {tag: $v, assets: [.files[] | select(startswith("linux-")) | {name: "node-\($v)-\(.).tar.xz", url: "https://nodejs.org/dist/\($v)/node-\($v)-\(.).tar.xz"}]}]'
  asset: "node-${tag}-linux-${arch}.tar.xz"
  rename: {amd64: "x64"}
  bin: {node: "*/bin/node"}
```

A locked tag that the URL no longer lists fails `apply`, run `che update`.
