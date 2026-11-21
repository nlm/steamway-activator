#!/usr/bin/env bash
# Installs steamway-activator as a per-user LaunchAgent on this computer.
set -euo pipefail

usage() {
	cat >&2 <<USAGE
Usage: $0 --zone ZONE --server-id ID --host IP --mac MAC [--rotate-key]
       [--idle-limit D] [--unreachable-limit D] [--interval D]
       [--wake-from SOURCES] [--binary PATH]

  --zone       Scaleway zone, e.g. fr-par-2
  --server-id  Scaleway instance server ID
  --host       the instance's address (flexible IP), used to read Sunshine's serverinfo
  --mac        the instance's MAC address (as sent by Moonlight), e.g. aa:bb:cc:dd:ee:ff
  --idle-limit, --unreachable-limit, --interval
               optional durations passed to the program, e.g. 5m, 10m, 60s
               (defaults: 5m, 10m, 60s); re-running without them resets the defaults
  --wake-from  optional extra sources allowed to send Wake PC besides this computer:
               comma-separated IPs or CIDRs (e.g. 192.168.1.0/24,10.8.0.5) or "any";
               default: this computer only
  --rotate-key ask for the Scaleway secret key again and replace the stored one
  --binary     path to the steamway-activator binary for this computer; default:
               next to this script, else at the repository root (where
               \`go build ./cmd/steamway-activator\` puts it)

The com.steamway.activator.plist template must sit next to this script.
USAGE
	exit 2
}

if [ "$(id -u)" -eq 0 ]; then
	echo "install.sh: do not run as root; it installs for the current user." >&2
	exit 1
fi

zone="" server_id="" host="" mac="" rotate=0 src_bin=""
extra=()
while [ $# -gt 0 ]; do
	case "$1" in
	--zone) [ $# -ge 2 ] || usage; zone=$2; shift 2 ;;
	--server-id) [ $# -ge 2 ] || usage; server_id=$2; shift 2 ;;
	--host) [ $# -ge 2 ] || usage; host=$2; shift 2 ;;
	--mac) [ $# -ge 2 ] || usage; mac=$2; shift 2 ;;
	--idle-limit | --unreachable-limit | --interval | --wake-from)
		[ $# -ge 2 ] && [ -n "$2" ] || usage
		extra+=("$1" "$2"); shift 2 ;;
	--rotate-key) rotate=1; shift ;;
	--binary) [ $# -ge 2 ] && [ -n "$2" ] || usage; src_bin=$2; shift 2 ;;
	*) usage ;;
	esac
done
if [ -z "$zone" ] || [ -z "$server_id" ] || [ -z "$host" ] || [ -z "$mac" ]; then
	usage
fi

here=$(cd "$(dirname "$0")" && pwd)
template="$here/com.steamway.activator.plist"
[ -f "$template" ] || { echo "install.sh: missing $template" >&2; exit 1; }
if [ -z "$src_bin" ]; then
	for candidate in "$here/steamway-activator" "$here/../../steamway-activator"; do
		if [ -f "$candidate" ]; then src_bin=$candidate; break; fi
	done
fi
if [ -z "$src_bin" ] || [ ! -f "$src_bin" ]; then
	cat >&2 <<MISSING
install.sh: steamway-activator binary not found${src_bin:+ at $src_bin}.
Build it first, from the repository root:
  go build -o steamway-activator ./cmd/steamway-activator
or pass its location with --binary PATH.
MISSING
	exit 1
fi

label=com.steamway.activator
bin="$HOME/.local/bin/steamway-activator"
plist="$HOME/Library/LaunchAgents/$label.plist"
log="$HOME/Library/Logs/steamway-activator.log"
domain="gui/$(id -u)"

mkdir -p "$HOME/.local/bin" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"

if [ "$rotate" -eq 1 ] || ! security find-generic-password -s steamway-activator -a scw-secret-key >/dev/null 2>&1; then
	# A bare -w makes security prompt for the key itself (hidden, twice), so the
	# secret never appears in a process argument list.
	echo "Enter the Scaleway secret key when security asks for the password."
	if ! security add-generic-password -U -s steamway-activator -a scw-secret-key -T /usr/bin/security -w; then
		echo "install.sh: could not store the key in the Keychain" >&2
		exit 1
	fi
fi

# Fill the template; values are XML-escaped.
xml() {
	local s=$1
	s=${s//&/&amp;}
	s=${s//</&lt;}
	s=${s//>/&gt;}
	printf '%s' "$s"
}
extra_xml=""
for a in ${extra[@]+"${extra[@]}"}; do
	extra_xml+="		<string>$(xml "$a")</string>"$'\n'
done
extra_xml=${extra_xml%$'\n'}
content=$(cat "$template")
content=${content//__EXTRA_ARGS__/"$extra_xml"}
for pair in "__BIN__=$bin" "__ZONE__=$zone" "__SERVER_ID__=$server_id" "__HOST__=$host" "__MAC__=$mac" "__LOG__=$log"; do
	name=${pair%%=*}
	value=$(xml "${pair#*=}")
	content=${content//"$name"/"$value"}
done
tmp_plist=$(mktemp "$plist.XXXXXX")
trap 'rm -f "$tmp_plist"' EXIT
printf '%s\n' "$content" >"$tmp_plist"
plutil -lint "$tmp_plist"

# Everything that can fail has passed: now replace the running agent.
launchctl bootout "$domain/$label" 2>/dev/null || true
# bootout can be asynchronous; wait until the service is gone.
for _ in 1 2 3 4 5 6 7 8 9 10; do
	launchctl print "$domain/$label" >/dev/null 2>&1 || break
	sleep 0.5
done

cp "$src_bin" "$bin"
chmod 755 "$bin"
xattr -d com.apple.quarantine "$bin" 2>/dev/null || true
mv "$tmp_plist" "$plist"

# Retry: bootstrap can still fail with I/O error 5 right after a bootout.
ok=0
for _ in 1 2 3 4 5 6; do
	if launchctl bootstrap "$domain" "$plist"; then ok=1; break; fi
	sleep 1
done
[ "$ok" -eq 1 ] || { echo "install.sh: launchctl bootstrap failed" >&2; exit 1; }

cat <<DONE

steamway-activator is installed and running.

Test:
  1. Stop the instance in the Scaleway console, then in Moonlight choose Wake PC on it.
  2. Watch the log:  tail -f "$log"

Uninstall:
  launchctl bootout $domain/$label
  rm "$plist" "$bin"
  security delete-generic-password -s steamway-activator -a scw-secret-key
DONE
