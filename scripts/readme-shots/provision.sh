#!/usr/bin/env bash
# Run as root inside the readme-shots VM (run.sh does it). Turns a stock
# Ubuntu cloud image into the demo box: lasso, herdr and the agents' CLIs at
# pinned versions, three users that stand in for three machines, and the
# services that keep lasso and each user's herdr up. Safe to run again.
#
# The fleet: `ubuntu` is lasso's own machine (local). `ci` and `ml` are the
# remote hosts buildbox and gpubox, each with its own herdr server, reached
# over real ssh at addresses of their own, so lasso lists them as separate
# machines. `laptop` points at an address nothing answers, as a laptop asleep
# in a bag would.
set -euo pipefail
# isb exec hands every user the compose file's environment, HOME=/home/ubuntu
# included; root's caches belong in root's home, not ubuntu's.
export HOME=/root

HERDR=0.9.3
# herdr and fnox publish no checksum file, so their digests are pinned here.
HERDR_SHA256=18a8dc65f1c2fa485884344356dea1cfd911c6f06cf46fa78e193f4087f4dba7
TTYD=1.7.7
OMP=18.8.9
CLAUDE=2.1.296
MISE=2026.10.7
UV=0.12.20
FNOX=1.39.0
FNOX_SHA256=0a623ed38907b9cb633d07593b4d19a4ba741a6a50d226eb243bd16f2f5ba81b
PLAYWRIGHT=1.55.0

export DEBIAN_FRONTEND=noninteractive
# sqlite3: lasso reads and writes a remote host's settings with it.
if [ ! -e /var/lib/readme-shots-apt ]; then
  apt-get update -q >/dev/null
  apt-get install -y -q --no-install-recommends git openssh-server tmux jq curl ca-certificates \
    zstd webp fonts-dejavu-core fonts-noto-color-emoji python3 golang-go sqlite3 >/dev/null
  touch /var/lib/readme-shots-apt
fi

# The local host's name is what the host menu and the Grid call it.
hostnamectl set-hostname devbox
grep -q ' devbox$' /etc/hosts || echo "127.0.1.1 devbox" >>/etc/hosts

# download URL SHA256: a temp file holding URL, refused unless its digest is SHA256.
download() {
  local tmp
  tmp=$(mktemp)
  curl -fsSL --retry 3 -o "$tmp" "$1"
  echo "$2  $tmp" | sha256sum -c --quiet - || { echo "checksum mismatch for $1" >&2; rm -f "$tmp"; exit 1; }
  echo "$tmp"
}
# fetch URL DEST SHA256: a checked binary installed at DEST.
fetch() {
  local tmp
  tmp=$(download "$1" "$3")
  install -m 0755 "$tmp" "$2"
  rm -f "$tmp"
}
# fetch_tar URL SHA256 MEMBER...: checked tarball members into /usr/local/bin.
fetch_tar() {
  local tmp dir
  tmp=$(download "$1" "$2")
  dir=$(mktemp -d)
  tar -xzf "$tmp" -C "$dir"
  shift 2
  for m in "$@"; do install -m 0755 "$dir/$m" "/usr/local/bin/${m##*/}"; done
  rm -rf "$tmp" "$dir"
}
# sum URL NAME: NAME's digest from a release's checksum file.
sum() {
  curl -fsSL --retry 3 "$1" | awk -v n="$2" '{ f = $2; sub(/^[*]/, "", f); sub(/^[.][/]/, "", f) } f == n { print $1; exit }'
}
# have BIN VERSION: BIN is installed and its --version mentions VERSION.
have() { [ -x "$1" ] && "$1" --version 2>/dev/null | grep -F -- "$2" >/dev/null; }

gh=https://github.com
have /usr/local/bin/herdr "$HERDR" || fetch "$gh/herdrdev/herdr/releases/download/v$HERDR/herdr-linux-x86_64" \
  /usr/local/bin/herdr "$HERDR_SHA256"
have /usr/local/bin/ttyd "$TTYD" || fetch "$gh/tsl0922/ttyd/releases/download/$TTYD/ttyd.x86_64" \
  /usr/local/bin/ttyd "$(sum "$gh/tsl0922/ttyd/releases/download/$TTYD/SHA256SUMS" ttyd.x86_64)"
have /usr/local/bin/omp "$OMP" || fetch "$gh/can1357/oh-my-pi/releases/download/v$OMP/omp-linux-x64" \
  /usr/local/bin/omp "$(sum "$gh/can1357/oh-my-pi/releases/download/v$OMP/SHA256SUMS.txt" omp-linux-x64)"
have /usr/local/bin/mise "$MISE" || fetch "$gh/jdx/mise/releases/download/v$MISE/mise-v$MISE-linux-x64" \
  /usr/local/bin/mise "$(sum "$gh/jdx/mise/releases/download/v$MISE/SHASUMS256.txt" "mise-v$MISE-linux-x64")"
if ! have /usr/local/bin/claude "$CLAUDE"; then
  base=https://downloads.claude.ai/claude-code-releases/$CLAUDE
  fetch "$base/linux-x64/claude" /usr/local/bin/claude \
    "$(curl -fsSL "$base/manifest.json" | jq -r '.platforms["linux-x64"].checksum')"
fi
uvtar=uv-x86_64-unknown-linux-gnu.tar.gz
have /usr/local/bin/uv "$UV" || fetch_tar "$gh/astral-sh/uv/releases/download/$UV/$uvtar" \
  "$(sum "$gh/astral-sh/uv/releases/download/$UV/$uvtar.sha256" "$uvtar")" \
  uv-x86_64-unknown-linux-gnu/uv uv-x86_64-unknown-linux-gnu/uvx
have /usr/local/bin/fnox "$FNOX" || fetch_tar \
  "$gh/jdx/fnox/releases/download/v$FNOX/fnox-x86_64-unknown-linux-gnu.tar.gz" "$FNOX_SHA256" fnox
# The lasso under test: the one `mise run build` just made.
install -m 0755 /in/lasso /usr/local/bin/lasso

# The fleet's users. ubuntu (uid 1000) already exists in the cloud image.
for u in ci ml; do
  id "$u" >/dev/null 2>&1 || useradd -m -s /bin/bash "$u"
done

# buildbox and gpubox get addresses of their own on a dummy interface. laptop's
# address is assigned to nothing, so ssh to it waits and times out.
ip link show demo0 >/dev/null 2>&1 || ip link add demo0 type dummy
ip link set demo0 up
for a in 10.77.0.2 10.77.0.3; do
  ip -4 addr show dev demo0 | grep -F " $a/" >/dev/null || ip addr add "$a/32" dev demo0
done
sed -i '/# readme-shots$/d' /etc/hosts
cat >>/etc/hosts <<'EOF'
10.77.0.2 buildbox # readme-shots
10.77.0.3 gpubox # readme-shots
10.77.0.9 laptop # readme-shots
EOF

# ubuntu reaches ci and ml with one key, as an operator's laptop would.
key=/home/ubuntu/.ssh/id_ed25519
if [ ! -e "$key" ]; then
  sudo -u ubuntu mkdir -p -m 700 /home/ubuntu/.ssh
  sudo -u ubuntu ssh-keygen -q -t ed25519 -N '' -C ubuntu@devbox -f "$key"
fi
for u in ci ml; do
  install -d -m 700 -o "$u" -g "$u" "/home/$u/.ssh"
  install -m 600 -o "$u" -g "$u" "$key.pub" "/home/$u/.ssh/authorized_keys"
done
systemctl enable --now ssh >/dev/null 2>&1

# The placeholders isb put in this VM's environment, for services and ssh
# sessions, which do not inherit it. They are placeholders, not the key: isb's
# proxy swaps the real value in on the way to api.deepseek.com.
: "${DEEPSEEK_API_KEY:?isb did not set the DeepSeek placeholder}"
install -d -m 755 /etc/readme-shots
cat >/etc/readme-shots/env <<EOF
DEEPSEEK_API_KEY=$DEEPSEEK_API_KEY
ANTHROPIC_AUTH_TOKEN=$ANTHROPIC_AUTH_TOKEN
EOF
chmod 644 /etc/readme-shots/env

# Per user: herdr's config, the agents' herdr integrations, the placeholders
# in every login shell, and a herdr server under systemd.
for u in ubuntu ci ml; do
  sudo -iu "$u" bash -s <<'EOF'
set -euo pipefail
# No welcome dialog over the first attach, and no sudo hint in every new shell.
mkdir -p ~/.config/herdr
[ -e ~/.config/herdr/config.toml ] || echo 'onboarding = false' >~/.config/herdr/config.toml
touch ~/.sudo_as_admin_successful
# herdr installs its integrations into the agents' own config directories.
mkdir -p ~/.omp/agent/extensions ~/.claude/hooks
herdr integration install omp >/dev/null
herdr integration install claude >/dev/null
grep -q readme-shots ~/.profile || cat >>~/.profile <<'P'
set -a; . /etc/readme-shots/env; set +a  # readme-shots
export PATH="$HOME/.local/share/mise/shims:$HOME/.local/bin:$PATH"  # readme-shots
P
EOF
done

cat >/etc/systemd/system/herdr@.service <<'EOF'
[Unit]
Description=herdr server for %i
After=network-online.target

[Service]
User=%i
WorkingDirectory=~
Environment=HOME=/home/%i
Environment=PATH=/home/%i/.local/share/mise/shims:/home/%i/.local/bin:/usr/local/bin:/usr/bin:/bin
EnvironmentFile=/etc/readme-shots/env
ExecStart=/usr/local/bin/herdr server
ExecStop=/usr/local/bin/herdr server stop
KillMode=mixed
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
cat >/etc/systemd/system/lasso.service <<'EOF'
[Unit]
Description=lasso (readme-shots demo)
After=herdr@ubuntu.service
Requires=herdr@ubuntu.service

[Service]
User=ubuntu
WorkingDirectory=/home/ubuntu
Environment=HOME=/home/ubuntu
Environment=PATH=/home/ubuntu/.local/share/mise/shims:/home/ubuntu/.local/bin:/usr/local/bin:/usr/bin:/bin
EnvironmentFile=/etc/readme-shots/env
ExecStart=/usr/local/bin/lasso -listen 127.0.0.1:8090 -disable-self-update
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now herdr@ubuntu herdr@ci herdr@ml >/dev/null 2>&1
systemctl enable lasso >/dev/null 2>&1
systemctl restart lasso

# Chromium's system libraries, for lasso's shared browser and for shoot.py.
# The browser itself is Playwright's, in ubuntu's cache, where lasso finds it.
uvx --from "playwright==$PLAYWRIGHT" playwright install-deps chromium >/dev/null
sudo -iu ubuntu uvx --from "playwright==$PLAYWRIGHT" playwright install chromium >/dev/null
echo "provisioned: lasso $(lasso version 2>/dev/null | head -n1), herdr $HERDR, omp $OMP"
