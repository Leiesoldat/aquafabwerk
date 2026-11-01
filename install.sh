#!/usr/bin/env bash
# Install the aquapx CoolerControl plugin. Run as root: sudo bash install.sh
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo bash $0" >&2; exit 1; }

src="$(cd "$(dirname "$0")" && pwd)"
dest=/var/lib/coolercontrol/plugins/aquapx

[ -x "$src/bin/aquapx-plugin" ] || {
	echo "missing $src/bin/aquapx-plugin; build first: go build -o bin/aquapx-plugin ./cmd/aquapx-plugin" >&2
	exit 1
}

install -d -m 0755 "$dest"
install -m 0755 "$src/bin/aquapx-plugin" "$dest/aquapx-plugin"
install -m 0644 "$src/manifest.toml" "$dest/manifest.toml"

# systemd (init_t) can't execute var_lib_t files under SELinux; label only the binary.
# Note: this label is lost if the plugin directory is deleted and recreated.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
	semanage fcontext -a -t bin_t "$dest/aquapx-plugin"
fi

systemctl restart coolercontrold
echo "installed to $dest"
