---
title: Migrating from APT
slug: /migrating-from-apt
description: Retire the former pairmux APT source safely and check for PATH shadowing.
---

# Migrating from APT / Debian packages

The former pairmux APT repository and `.deb` distribution are being retired for v0.5.3. Use
[PyPI/uv, Homebrew, or a manual archive](./getting-started.mdx#install) for pairmux instead. This
does not retire APT for system dependencies: keep **tmux >= 3.2** installed. The PyPI installer
does not change your APT configuration or remove system packages.

## 1. Inspect the former configuration

The former `pairmux-apt` README's enrollment snippet created these exact paths:

| Item | Former path or name |
| --- | --- |
| APT source | `/etc/apt/sources.list.d/pairmux.sources` |
| Origin pin | `/etc/apt/preferences.d/pairmux.pref` |
| Keyring file | `/usr/share/keyrings/pairmux-archive-keyring.pgp` |
| Keyring package | `pairmux-archive-keyring` |
| Binary package | `pairmux` |

The source referenced `https://treeleaves30760.github.io/pairmux-apt` and the keyring above. The pin
used `Package: *`, `Pin: origin treeleaves30760.github.io`, and `Pin-Priority: 100`. That broad
host-level pin is another reason not to leave it behind after retiring this source.

Use read-only inspection first; do not blindly delete similarly named files or a shared keyring:

```bash
grep -R -n -E 'pairmux-apt|pairmux-archive-keyring|treeleaves30760\.github\.io' \
  /etc/apt/sources.list /etc/apt/sources.list.d /etc/apt/preferences /etc/apt/preferences.d 2>/dev/null
dpkg-query -W -f='${Package} ${Version} ${Status}\n' pairmux pairmux-archive-keyring
dpkg-query -L pairmux-archive-keyring
dpkg-query -S /usr/share/keyrings/pairmux-archive-keyring.pgp
type -a pairmux
```

Missing files or packages are normal if you used only a direct `.deb` or a different installer.
Check for locally renamed `.sources` / `.list` entries and other sources using the same keyring.

## 2. Move to a supported installation and retire the old source

1. Back up the inspected source and pin files. Using your normal administrative procedure, disable
   or remove **only** the confirmed pairmux source entry and pairmux origin pin. If a file contains
   unrelated entries, edit only the pairmux entry rather than removing the file. Do this before
   refreshing APT metadata so it no longer contacts the retired endpoint.
2. Install from PyPI/uv or another supported channel in [Getting Started](./getting-started.mdx#install).
   If the old command occupies your chosen executable directory, select an unused
   `PAIRMUX_INSTALL_DIR` or review the old package's removal first; the installer does not
   force-overwrite a command it does not own. Run the new executable by its **absolute path** to
   verify `version` and `doctor`, not merely the command found on PATH.
3. Review the installed `pairmux` and `pairmux-archive-keyring` packages for removal through your
   package manager. Preview the transaction before accepting it:

   ```bash
   apt-get --simulate remove pairmux pairmux-archive-keyring
   ```

   Remove only packages that are actually installed. Do not accept removal of tmux or unrelated
   packages, and do not blindly use `autoremove`: tmux may have been marked as an automatic
   dependency of the old package. Keep it installed (mark it manually installed if needed).
4. The keyring package may own `/usr/share/keyrings/pairmux-archive-keyring.pgp`; let package removal
   clean up its owned file. If the bootstrap left an unowned copy, remove it only after confirming
   no remaining source references it. Refresh APT metadata once cleanup is complete. There is no
   destructive cleanup one-liner here: review and apply system changes yourself.

## 3. Check PATH shadowing

```bash
export PATH="$HOME/.local/bin:$PATH" # use your chosen executable directory instead if different
hash -r
type -a pairmux
command -v pairmux
pairmux version
pairmux doctor
```

An old `/usr/bin/pairmux`, Homebrew command, alias, function, or earlier user-bin directory can
shadow the new installation. Check its ownership/location before changing it; do not manually
unlink package-managed commands. The installer verifies its own absolute executable path, but a
pipeline cannot change the invoking shell's PATH or aliases. Any lasting PATH edit is your choice,
not an installer side effect.
