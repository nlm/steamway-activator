# steamway-activator

Play on a Scaleway GPU instance without paying for it while you're not playing.

- Press **Wake PC** in [Moonlight](https://moonlight-stream.org/): the instance starts.
- Quit your game: about 5 minutes later, the instance is powered off.
- Notifications tell you when it starts, when it's ready, and when it's powered off.

It runs quietly on your Mac. Nothing is installed on the instance.

```
Moonlight --Wake PC--> UDP 9 --> steamway-activator --HTTPS--> Scaleway API (start / poweroff)
                                        |
                                        +--HTTP 47989--> Sunshine serverinfo (is an app running?)
```

## Before you start

You need:

- A Mac with Moonlight, already paired with your instance.
- A Scaleway GPU instance running [Sunshine](https://github.com/LizardByte/Sunshine), with a
  **flexible IP** (so its address never changes).
- A **Scaleway API secret key** (IAM → API keys). Give it access to Instances only, ideally in a
  project that holds just this instance.
- Go 1.26 to build the program (`brew install go`).

In Moonlight, open Settings and turn on **"Quit app after ending stream"**. Without it, closing
Moonlight leaves the game running and the instance never powers off.

## Install

**1. Collect four values**

| Value | Where to find it |
| --- | --- |
| Zone | Scaleway console, instance page (e.g. `fr-par-2`) |
| Server ID | Scaleway console, instance page |
| Address | The instance's flexible IP |
| MAC address | See [Finding the MAC address](#finding-the-mac-address) |

**2. Build and install** — in Terminal, from this folder:

```
go build -o steamway-activator ./cmd/steamway-activator
deploy/macos/install.sh --zone <zone> --server-id <server-id> --host <address> --mac <mac>
```

The installer asks for your Scaleway secret key (twice) and stores it in the macOS Keychain.
The program then starts automatically at every login.

**3. Test it**

1. Allow notifications: run this, and if no notification appears, enable **Script Editor** in
   System Settings → Notifications.
   ```
   osascript -e 'display notification "It works" with title "steamway-activator"'
   ```
2. Stop the instance, then press **Wake PC** in Moonlight. You should see "Starting the
   instance…", then "Instance ready: open Moonlight" within a few minutes.
3. Don't start a game: about 5 minutes later you should see "Instance powered off".

Something wrong? Look at the log: `tail -f ~/Library/Logs/steamway-activator.log`

## Everyday use

1. Press **Wake PC** in Moonlight.
2. Wait for "Instance ready", then connect and play.
3. When you're done, end the stream. The instance powers off by itself about 5 minutes later.

## Notifications

| Notification | Meaning |
| --- | --- |
| Starting the instance… | Wake PC worked |
| Instance ready: open Moonlight | You can connect |
| Instance powered off: … | Done, with the reason |
| Instance didn't start: … | Scaleway refused, e.g. no GPU available right now; try again later |
| … automatic shutdown is disabled | Something needs fixing (key, server ID, Keychain): the instance will **not** power off by itself until then |
| Wake PC is disabled … | Another program uses UDP port 9; shutdowns still work |

## Good to know

- **Only end the stream properly.** If the stream is cut (lid closed, Wi-Fi lost, Moonlight
  crash), the game keeps running and so does the instance, until you reconnect and quit the game.
- **Your Mac must be awake.** Nothing powers the instance off while the Mac sleeps or is off; it
  catches up as soon as the Mac wakes.
- **No internet on the Mac? Nothing happens.** The program never powers off the instance because
  of a problem on your side.
- **Playing away from home** only works if your security group allows your current IP. Otherwise
  the instance is powered off after 10 minutes.
- **It only watches after a Wake PC.** An instance started from the Scaleway console (or
  anything else) is left alone: no polling, no notifications, no power-off. Watching starts when
  you press Wake PC (whether that starts the instance or it was already running), and stops once
  the instance is powered off or seen stopped.
- **Restarting the program forgets the session.** If the Mac reboots or you log out while the
  instance runs, it is no longer watched: press Wake PC again to watch it.
- The Scaleway console always works as a fallback to start or stop the instance.

## Options

Add these to the `install.sh` command (run it again to change them):

| Option | Default | What it does |
| --- | --- | --- |
| `--zone` | required | Scaleway zone, e.g. `fr-par-2` |
| `--server-id` | required | Scaleway instance server ID |
| `--host` | required | The instance's address |
| `--mac` | required | The instance's MAC address |
| `--idle-limit` | `5m` | How long with no game running before powering off |
| `--unreachable-limit` | `10m` | How long Sunshine may stay silent (while the instance runs) before powering off |
| `--interval` | `60s` | How often it checks |
| `--wake-from` | this Mac only | Also accept Wake PC from other devices, e.g. `192.168.1.0/24`, or `any` (see security) |
| `--rotate-key` | | Ask for a new Scaleway key |
| `--binary` | | Use a binary from another location |

Advanced flags of the program itself (not exposed by `install.sh`):

| Flag | Default | What it does |
| --- | --- | --- |
| `--listen` | `0.0.0.0:9` | UDP address for Wake PC packets |
| `--keychain-service` | `steamway-activator` | Keychain service holding the key |
| `--keychain-account` | `scw-secret-key` | Keychain account holding the key |
| `--api-url` | `https://api.scaleway.com` | Scaleway API URL (for tests) |

## Finding the MAC address

On the instance, run `ip link` and look at the `link/ether` line of the main network interface.

Or, on your Mac, run this and press Wake PC in Moonlight (stop steamway-activator first if it's
installed: `launchctl bootout gui/$(id -u)/com.steamway.activator`):

```
python3 - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("", 9))
print("Press Wake PC in Moonlight...")
while True:
    d, a = s.recvfrom(2048)
    print("MAC:", d[6:12].hex(":"))
PY
```

## Uninstall

```
launchctl bootout gui/$(id -u)/com.steamway.activator
rm ~/Library/LaunchAgents/com.steamway.activator.plist ~/.local/bin/steamway-activator
security delete-generic-password -s steamway-activator -a scw-secret-key
```

## Security

- Your Scaleway key is kept in the macOS Keychain, never in a file. Other programs running under
  your account could still read it, so give the key access to Instances only, in a dedicated
  project.
- By default, only Moonlight on this Mac can wake the instance. `--wake-from` opens this to other
  devices: anyone on an allowed network who knows the MAC address (Moonlight broadcasts it) could
  then start your instance or keep it running. Use it only on networks you trust.
- The program reads Sunshine's public status page (`serverinfo`, plain HTTP). Someone able to
  intercept traffic between your Mac and the instance could fake it.

<details>
<summary><b>How it works in detail</b></summary>

**Wake PC.** Moonlight sends a Wake-on-LAN packet (UDP port 9). The program checks the
instance's state on Scaleway:

| State | Action |
| --- | --- |
| stopped, stopped in place | Start it |
| starting | Nothing, it's on its way |
| running | Starts watching it if not already, and resets the idle clock |
| stopping | Waits up to 3 minutes, then starts it |
| locked | Notification: Scaleway won't start it |

Repeated packets within 10 seconds of a start are ignored (Moonlight sends several).

**Automatic power-off.** Only after a Wake PC: every minute the program asks
Scaleway for the state and, if the instance is running, asks Sunshine whether a game (app) is
running. Until then it makes no request at all. It powers off (`poweroff`,
which releases the GPU and stops billing) when:

- no app has been running for the idle limit, counted from Sunshine's first answer after boot;
- Sunshine hasn't answered for the unreachable limit while Scaleway says the instance runs;
- the instance is "stopped in place" (still billed).

It stops watching after powering off, or when it sees the instance stopping or stopped (or still
stopped 3 minutes after the start).

If Scaleway can't be reached, it does nothing and tries again. After the Mac slept, the time
asleep counts as idle, but only once Sunshine confirms no game is running.

</details>
