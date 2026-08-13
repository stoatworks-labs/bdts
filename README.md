# bdts — Tailscale settings in birdUI, for the BirdDog PLAY

> **AI-assisted project.** This codebase was created with [Claude Code](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author. **Installed and running on a real BirdDog
> PLAY (firmware 1.0.30)**: the System page is patched and served, the web UI survived the restart
> with its firmware-upload form intact, the panel's API answers off real tailscaled 1.102.2 over
> both the LAN and the tailnet, and the auth gate refuses a missing or forged session. That is
> **one unit on one firmware**. Two things are still unproven and are called out in
> [Verifying on hardware](#verifying-on-hardware): **no login has been completed through the panel**
> (the test unit was already signed in, and testing it would have logged a live node off the
> tailnet), and **the browser sending its session cookie to the panel's port is reasoned from how
> cookies are scoped, not measured** — if that turns out to be wrong, writes refuse from a
> logged-in browser and the gate needs an explicit password field instead.

Adds a **Tailscale** panel to the System page of the PLAY's web UI, so a device
can be signed in to a tailnet — and configured afterwards — from the browser.

Before this, a PLAY patched with Tailscale came up installed but unauthenticated,
and the only way to finish was to SSH in on port 9031 and run `tailscale up` by
hand. That is a real gap rather than a convenience: the public
[PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com) deliberately
bakes **no auth key** into the firmware package it builds, because a key in a
package downloaded through a browser is a key in cleartext. So every device it
produces needs an interactive login it had no way to offer.

```
┌───────────────────────────┐         ┌────────────────────────────┐
│ birdUI  :80  /settings    │         │ bdts --serve :8092         │
│  … Password Settings      │         │  /api/status   (open)      │
│  … System Update          │  fetch  │  /api/up       (gated)     │
│  … UI Mode                │────────▶│  /api/down     (gated)     │
│  ▸ Tailscale       ← ours │ +cookie │  /api/logout   (gated)     │
│  ▸ Tailscale Settings     │         │  /api/settings (gated)     │
└───────────────────────────┘         └─────────────┬──────────────┘
                                                    │ tailscale --socket=…
                                                    ▼
                                       bd-tailscaled.service
                                       (userspace networking)
```

## What it does

**Signing in.** With no auth key, the panel starts an interactive login and
shows the `login.tailscale.com` link as soon as tailscaled produces one; the
page then updates itself when the login completes. With a key, it connects
directly — useful for unattended setup, and the panel says plainly that a key
stays reusable until it expires.

**Settings, afterwards.** Machine name, MagicDNS, accept-subnet-routes,
Tailscale SSH, and choosing an exit node. Changes go through `tailscale set`,
not `tailscale up` — running `up` on a live node re-runs the whole login flow
and silently resets anything not named on the command line.

**Being honest about the hardware.** The PLAY's 4.4 kernel has no TUN driver and
no loadable modules, so Tailscale runs in userspace-networking mode. This node
therefore cannot advertise routes or act as an exit node, and the panel says so
rather than offering switches that would fail. Using someone else's exit node is
unaffected. Inbound still works: SSH, `:80` and the `:8080` API are all reachable
over the tailnet, because the netstack proxies inbound TCP to localhost.

## Authentication — and why this differs from bdcam

Reads are open. Writes require a valid birdUI session.

bdcam's settings API is unauthenticated on the reasonable grounds that the PLAY's
own REST API on `:8080` already is, so it adds no new class of exposure. That
argument does not carry over here. Anyone able to reach an ungated Tailscale API
could join the device to **their** tailnet and keep remote access to it
indefinitely, or log it out of yours. That is a different kind of thing from
changing a frame rate.

The gate needs no second password and no new credential store, because **cookies
are scoped by host and not by port**: the `BirdDogSession` cookie the browser
already holds for the device is sent to this daemon too, as long as the panel
fetches with `credentials: 'include'` and the daemon answers with a concrete
`Access-Control-Allow-Origin` plus `Allow-Credentials`. A wildcard origin is not
permitted to carry credentials — it would silently strip the cookie and make
every write look unauthenticated — so the origin is echoed instead.

Validating the cookie means asking whatever issued it: the daemon re-requests
`/settings` from birddog-web-ui on `127.0.0.1` with the caller's cookie and sees
whether it gets the page or the login screen.

**The trap that makes this more than theatre:** that check only proves something
if the page is actually protected. On a device with no birdUI password, the
unauthenticated request succeeds too, and a naive implementation would wave
everyone through while looking rigorous. So every check is **differential** — it
probes without the cookie as well, and if that also comes back authenticated it
reports the device as unprotected and **refuses the write** rather than
pretending to have gated it. `--allow-unprotected` overrides that for a trusted
lab network, and the panel says loudly when it is on.

Everything fails closed: an unreachable web UI, an unrecognised page, a redirect
to `/login`, a missing cookie. There are tests for each.

## The template patch, and the risk it carries

The System page is `/srv/birddog-web-ui/settings.html` — **not**
`system-settings.html`, which is older markup the current firmware no longer
serves. Both are on disk; the nav links to `/settings`. Patching the wrong one
is a silent no-op that looks exactly like a broken patch.

That page also carries the **System Update** form — the firmware upload path,
which is how you recover a device from a bad install. A malformed edit does not
fail when it is written; it fails when birddog-web-ui parses the template, and
it takes the whole web UI with it. So, following bdcam's approach:

- the patch is tested Go, not `sed` in an installer;
- the insertion is one empty `<div>` and one `<script>` tag — **no template
  actions at all**, asserted by a test that counts `{{` and `}}` before and
  after;
- a test parses the patched page with `html/template` and requires it to succeed;
- the anchor is **`</body>`**, and the tests run against the real System page
  from *every* firmware seen so far. This one was learned the hard way: the
  anchor was originally `<!-- End UI Mode -->`, verified unique on 1.0.32 and
  1.0.34 (byte-identical to each other) — and **absent from 1.0.30**, which is
  what the test unit actually runs, because the UI Mode section did not exist
  yet. Every test passed and the patch refused to apply on real hardware.
  Anchor on structure the document must have, not on a feature a version might
  not ship;
- unfamiliar markup is **refused**, not patched on a guess;
- patching is idempotent, unpatching restores the file byte-identically, and a
  pristine `.bdts-stock` backup is written before anything is touched — and
  never overwritten by a later install.

All the behaviour lives in the static JS, served from `/static/`, outside the
template system entirely. So iterating on the panel never risks the parse again.

## Usage

```bash
./build.sh                       # -> dist/bdts-linux-arm64 (static, no cgo)
go test ./...                    # patch, gate and CLI behaviour, on the host
```

On the device:

```bash
bdts --patch-ui                  # add the panel; then restart BirdDogWebUI
bdts --serve :8092               # the API, as bd-tailscale-ui.service
bdts --unpatch-ui                # remove it, restoring the stock page
bdts --restore-ui                # roll back from the .bdts-stock backup
```

Two things learned the hard way by bdcam, which apply identically here:

- the systemd unit is **`BirdDogWebUI`**; `birddog-web-ui` is the binary.
  Restarting the wrong name is a silent no-op.
- consequently **an HTTP 200 after the restart proves nothing** — it is answered
  by the process that never reloaded. Require the pid to have changed.

## Verifying on hardware

Done on one unit, firmware 1.0.30:

- ✅ `--patch-ui` against the live `/srv/birddog-web-ui`, then
  `systemctl restart BirdDogWebUI`: pid changed 311 → 1752, `/login` and
  `/dashboard` both 200, **the System Update form intact**, and a
  `settings.html.bdts-stock` backup written.
- ✅ `/static/bdts-tailscale.js` served (200).
- ✅ The API answers off real tailscaled 1.102.2, over the LAN *and* the
  tailnet: `tun:false` and `exit_node_capable:false` confirm userspace
  networking, so the panel correctly declines to offer route advertising.
- ✅ `tailscale debug prefs` exists in 1.102.2 and parses, so the toggles show
  real state rather than falling back to blank.
- ✅ The gate returns 403 for a missing **and** a forged session, and reports
  `device_protected:true` — this unit does have a birdUI password, so the
  differential probe has something real to detect.
- ✅ Patch/unpatch round-trips **byte-identically** against the unit's own
  `settings.html`.

Still outstanding:

1. **Confirm the session cookie actually arrives from a browser** — the
   load-bearing assumption, and the only one left that could change the design.
   Everything server-side is verified; this needs a logged-in browser, because
   it depends on the browser choosing to attach `BirdDogSession` to a
   cross-port `credentials: 'include'` fetch. If it does not, writes refuse with
   "log in to birdUI first" from a perfectly good session, and the gate needs an
   explicit password field instead.
2. **Sign in interactively through the panel** and check the login URL appears,
   then that the page notices completion by itself. Not yet done because the
   test unit was already signed in and testing it would have logged a live node
   off the tailnet — which is how that unit is reached.

Note `GET /settings` unauthenticated redirects to `/dashboard`, not `/login`.
The gate treats any 3xx as unauthenticated, which is why that works.
5. Check `tailscale debug prefs` really is present in 1.102.2 — the settings
   toggles read their current state from it, and fall back to blank if not.
6. Roll back with `--unpatch-ui` and confirm the page is byte-identical to
   `/userdata/settings.html.orig` if BirdDog ships one, or to the `.bdts-stock`
   backup otherwise.

## Where this sits

`bdts` is a sibling checkout alongside `bdcam` and `bd-play-usb-player`. The
firmware packager in `birddog-re/tools/fwbuild` installs it as part of the
Tailscale payload — not behind a separate flag, because a Tailscale install that
still requires SSH to finish is the problem this solves.

The reverse engineering that justifies the claims here lives in `birddog-re`,
which is **local-only and must stay that way**: its history contains a recovered
vendor AES key and decrypted firmware. Do not copy key material or vendor
firmware into this repo.

## Licence

MIT — see [LICENSE](LICENSE). Tailscale itself is not redistributed here; the
firmware packager fetches it, and its BSD-3 notice obligation is carried there.

## Ports on this device

| Port | Service |
|---|---|
| 80 | birdUI (stock) |
| 8080 | BirdDog's own REST API (stock, unauthenticated) |
| 8090 | `bd-cam-api` — bdcam's UVC Converter tab |
| 8091 | `bd-play` — bdplay's USB media player |
| **8092** | **`bd-tailscale-ui` — this** |

Picked to avoid the three that are already taken; 8091 in particular is bdplay's
and would have collided silently, with whichever service started second failing
to bind and its panel simply never answering.
