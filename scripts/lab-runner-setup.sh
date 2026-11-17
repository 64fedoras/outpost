#!/bin/sh
# Sets up a machine as the home-lab Gitea Actions runner (see testing/README.md, "Lab checks").
# Run it as root on Debian or Ubuntu:
#
#   sudo sh scripts/lab-runner-setup.sh --instance http://192.0.2.20:3000 --token-file ~/token
#   sudo GITEA_RUNNER_REGISTRATION_TOKEN=... sh scripts/lab-runner-setup.sh --instance https://gitea.example.local
#
# Options:
#   --instance URL     Gitea address, with http:// or https:// (required on the first run)
#   --token-file FILE  file holding the runner registration token (or set GITEA_RUNNER_REGISTRATION_TOKEN;
#                      a token on the command line would show up in the process list, so it is not offered)
#   --name NAME        runner name shown in Gitea (default: lab-runner)
#   --labels LABELS    default: lab-runner:host (jobs run directly on this machine, as the gitea-runner user)
#   --hostname NAME    set this machine's hostname (default: gitea-runner)
#   --keep-hostname    leave the hostname alone
#   --version X.Y.Z    gitea-runner version to install (default below)
#   NODE_MAJOR=N       Node.js major version to require (default 24); the NodeSource repository is used when it is missing
#   GITEA_RUNNER_BASE_URL  download folder if the runner moves (default https://dl.gitea.com/gitea-runner;
#                      files are <base>/<version>/gitea-runner-<version>-linux-<arch> and .sha256)
#
# It installs curl, git, Python 3 and Node.js 24 (the checkout action needs Node), installs the
# gitea-runner binary after checking its SHA-256, creates the gitea-runner user, registers the runner
# (only if it is not registered yet), and starts it as the systemd service gitea-runner. Running it
# again is safe: finished steps are skipped. The token is never stored by this script.
#
# Nothing here assumes a particular network: addresses, hostname, name, labels, versions and download
# folder are all options or environment variables. Behind a proxy, run with sudo -E so curl and apt see it.
#
# Host jobs run without isolation as the gitea-runner user: only point this runner at a mirror you control.
# Untested on a real machine at the time of writing: read it before running it.
set -eu

VERSION=5.4.1
NAME=lab-runner
NODE_MAJOR=${NODE_MAJOR:-24}
LABELS=lab-runner:host
INSTANCE=""
TOKEN_FILE=""
NEW_HOSTNAME=gitea-runner
BASE_URL=${GITEA_RUNNER_BASE_URL:-https://dl.gitea.com/gitea-runner}
HOME_DIR=/var/lib/gitea-runner
CONF=/etc/gitea-runner/config.yaml

die() { echo "lab-runner-setup: $*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --instance) [ $# -ge 2 ] || die "--instance needs a value"; INSTANCE=$2; shift 2 ;;
    --token-file) [ $# -ge 2 ] || die "--token-file needs a value"; TOKEN_FILE=$2; shift 2 ;;
    --name) [ $# -ge 2 ] || die "--name needs a value"; NAME=$2; shift 2 ;;
    --labels) [ $# -ge 2 ] || die "--labels needs a value"; LABELS=$2; shift 2 ;;
    --hostname) [ $# -ge 2 ] || die "--hostname needs a value"; NEW_HOSTNAME=$2; shift 2 ;;
    --keep-hostname) NEW_HOSTNAME=""; shift ;;
    --version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift 2 ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

[ "$(id -u)" = 0 ] || die "run this as root (sudo sh $0 ...)"
command -v apt-get >/dev/null 2>&1 || die "this script supports Debian and Ubuntu (apt-get) only"
case "$NODE_MAJOR" in ""|*[!0-9]*) die "NODE_MAJOR must be a number" ;; esac
case "$VERSION" in [0-9]*.[0-9]*.[0-9]*) ;; *) die "--version must look like 5.4.1" ;; esac
case "$NAME" in *[!A-Za-z0-9._-]*|"") die "--name may only use letters, digits, . _ -" ;; esac
case "$LABELS" in *[!A-Za-z0-9._:,/-]*|"") die "--labels has odd characters" ;; esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU: $(uname -m)" ;;
esac

# ---- hostname ----------------------------------------------------------------------------------
if [ -n "$NEW_HOSTNAME" ]; then
  case "$NEW_HOSTNAME" in *[!A-Za-z0-9-]*|-*|"") die "--hostname may only use letters, digits and -" ;; esac
  step "hostname"
  if [ "$(hostname)" = "$NEW_HOSTNAME" ]; then
    echo "already $NEW_HOSTNAME"
  else
    hostnamectl set-hostname "$NEW_HOSTNAME"
    # Debian and Ubuntu map the machine's own name to 127.0.1.1; keep sudo from complaining.
    if grep -q '^127\.0\.1\.1' /etc/hosts; then
      sed -i "s/^127\.0\.1\.1.*/127.0.1.1 $NEW_HOSTNAME/" /etc/hosts
    else
      echo "127.0.1.1 $NEW_HOSTNAME" >> /etc/hosts
    fi
    echo "hostname is now $NEW_HOSTNAME (new SSH sessions show it)"
  fi
fi

# ---- packages ----------------------------------------------------------------------------------
step "packages (curl, git, Python 3)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates git python3 >/dev/null

step "Node.js $NODE_MAJOR"
node_major() { node --version 2>/dev/null | sed -n 's/^v\([0-9]*\)\..*/\1/p'; }
if [ "$(node_major)" -ge "$NODE_MAJOR" ] 2>/dev/null; then
  echo "Node $(node --version) is already installed"
else
  # NodeSource's installer adds their apt repository; it is a script from their site run as root.
  curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - >/dev/null
  apt-get install -y -qq nodejs >/dev/null
  echo "installed Node $(node --version)"
fi

# ---- gitea-runner binary -----------------------------------------------------------------------
step "gitea-runner $VERSION"
if command -v gitea-runner >/dev/null 2>&1 && gitea-runner --version 2>/dev/null | grep -qF "$VERSION"; then
  echo "already installed"
else
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  F=gitea-runner-$VERSION-linux-$ARCH
  U=$BASE_URL/$VERSION
  curl -fsSL -o "$TMP/$F" "$U/$F" || die "download failed: $U/$F (is --version right?)"
  curl -fsSL -o "$TMP/$F.sha256" "$U/$F.sha256" || die "checksum download failed"
  (cd "$TMP" && sha256sum -c "$F.sha256") || die "checksum does not match: not installing"
  install -m 0755 "$TMP/$F" /usr/local/bin/gitea-runner
  echo "installed /usr/local/bin/gitea-runner"
fi

# ---- user, config, registration ----------------------------------------------------------------
step "gitea-runner user and config"
id gitea-runner >/dev/null 2>&1 || useradd --system --home-dir "$HOME_DIR" --create-home gitea-runner
mkdir -p /etc/gitea-runner
if [ ! -f "$CONF" ]; then
  gitea-runner config init -c "$CONF" >/dev/null 2>&1 \
    || gitea-runner generate-config > "$CONF" \
    || die "could not create the runner config (this gitea-runner version has neither 'config init' nor 'generate-config')"
fi
chown gitea-runner "$CONF"
echo "config: $CONF"

step "registration"
if [ -f "$HOME_DIR/.runner" ]; then
  echo "already registered ($HOME_DIR/.runner exists); delete that file and the runner in Gitea to register again"
  echo "(a runner you set up by hand elsewhere is not detected: this script registers its own)"
else
  [ -n "$INSTANCE" ] || die "--instance is needed to register (for example http://192.0.2.20:3000)"
  case "$INSTANCE" in http://*|https://*) ;; *) die "--instance must start with http:// or https://" ;; esac
  TF=$(mktemp)
  trap 'rm -rf "${TMP:-}" "$TF"' EXIT
  if [ -n "$TOKEN_FILE" ]; then
    [ -r "$TOKEN_FILE" ] || die "cannot read $TOKEN_FILE"
    tr -d '\r\n ' < "$TOKEN_FILE" > "$TF"
  elif [ -n "${GITEA_RUNNER_REGISTRATION_TOKEN:-}" ]; then
    printf '%s' "$GITEA_RUNNER_REGISTRATION_TOKEN" > "$TF"
  else
    die "give the registration token with --token-file FILE or GITEA_RUNNER_REGISTRATION_TOKEN"
  fi
  [ -s "$TF" ] || die "the token is empty"
  chown gitea-runner "$TF"
  chmod 600 "$TF"
  # Run from the runner's own folder: the .runner file is written to the working directory.
  (cd "$HOME_DIR" && runuser -u gitea-runner -- gitea-runner register --no-interactive \
      -c "$CONF" --instance "$INSTANCE" --token-file "$TF" --name "$NAME" --labels "$LABELS") \
    || die "registration failed (check the address is reachable from this machine and the token is current)"
  rm -f "$TF"
fi

# ---- service -----------------------------------------------------------------------------------
step "systemd service"
cat > /etc/systemd/system/gitea-runner.service <<'EOF'
[Unit]
Description=Gitea Actions runner
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/gitea-runner daemon --config /etc/gitea-runner/config.yaml
WorkingDirectory=/var/lib/gitea-runner
User=gitea-runner
Group=gitea-runner
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable gitea-runner >/dev/null 2>&1
systemctl restart gitea-runner
sleep 2
systemctl is-active gitea-runner >/dev/null || die "the service did not start: journalctl -u gitea-runner -n 50"

echo
echo "Done. The runner '$NAME' should show as Idle in Gitea (repo Settings, Actions, Runners)."
echo "Next, in the mirror's Settings, Actions, Secrets:"
echo "  OUTPOST_LAB_URL          the controller address WITH https://, for example https://192.0.2.10:8443"
echo "                           (the controller only serves https; plain http gets a bare 400)"
echo "  OUTPOST_LAB_TOKEN        an API token from the controller's Settings, API tokens"
echo "  OUTPOST_LAB_FINGERPRINT  optional: the 'certificate not verified' line in the first run's log prints the value"
echo "Then run the Lab workflow. Keep this runner's only label as $LABELS: Gitea also lists the mirror's"
echo "GitHub workflows, and a ubuntu-24.04 label would make it try the release build."
echo "Tip: set the mirror's sync interval to 10m so merged changes show up quickly."
