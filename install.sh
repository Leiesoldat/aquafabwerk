#!/usr/bin/env bash
# Install the aquapx CoolerControl plugin. Run as root: sudo bash install.sh
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo bash $0" >&2; exit 1; }

src="$(cd "$(dirname "$0")" && pwd)"
dest=/var/lib/coolercontrol/plugins/aquapx

# Both binaries are built as your user (root often has no Go toolchain), then installed here.
for b in aquapx-plugin aquapx; do
	[ -x "$src/bin/$b" ] || {
		echo "missing $src/bin/$b; build first:" >&2
		echo "  go build -o bin/aquapx-plugin ./cmd/aquapx-plugin && go build -o bin/aquapx ./cmd/aquapx" >&2
		exit 1
	}
done

# The CLI goes to the invoking user's ~/.local/bin (on PATH on most desktops, and writable on
# image-based distros like Bazzite / Fedora Atomic), installed alongside the plugin so the two
# never drift apart. Under sudo, ~ would be root's home, so use SUDO_USER's.
cli_user="${SUDO_USER:-}"
if [ -n "$cli_user" ] && [ "$cli_user" != root ]; then
	cli_home="$(getent passwd "$cli_user" | cut -d: -f6)"
	cli_dest="$cli_home/.local/bin"
else
	cli_user=root
	cli_dest=/usr/local/bin
fi

install -d -m 0755 "$dest"
install -m 0755 "$src/bin/aquapx-plugin" "$dest/aquapx-plugin"
install -m 0644 "$src/manifest.toml" "$dest/manifest.toml"
if [ "$cli_user" = root ]; then
	install -d -m 0755 "$cli_dest"
	install -m 0755 "$src/bin/aquapx" "$cli_dest/aquapx"
else
	cli_group="$(id -gn "$cli_user")"
	install -d -m 0755 -o "$cli_user" -g "$cli_group" "$cli_dest"
	install -m 0755 -o "$cli_user" -g "$cli_group" "$src/bin/aquapx" "$cli_dest/aquapx"
fi
if command -v restorecon >/dev/null 2>&1; then
	restorecon "$cli_dest/aquapx" || true
fi

# systemd (init_t) can't execute var_lib_t files under SELinux; label only the binary.
# Note: this label is lost if the plugin directory is deleted and recreated.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
	semanage fcontext -a -t bin_t "$dest/aquapx-plugin"
fi

systemctl restart coolercontrold
echo "installed plugin to $dest and CLI to $cli_dest/aquapx"
