# Notes

Working notes for this repo: status, decisions, and the traps that have actually bitten.
Migrated out of Claude Code's memory on 2026-08-24, so they are written in the first
person and dated by when each thing was learned — that date is usually the useful part.

Cross-cutting notes that are not specific to this repo live in
[fleet-notes](https://github.com/stoatworks-labs/fleet-notes).

*bdts — Tailscale sign-in and settings on birdUI's System page for the BirdDog PLAY; PUBLIC repo, LIVE in the Patcher, running on unit .42*

`~/Projects/bdts` — **PUBLIC** (github.com/stoatworks-labs/**bdts**, MIT, branch
`main`), **LIVE 2026-08-13**: shipped by [birddog play patcher](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/docs/NOTES.md) (`birddog-play-patcher`) with
the Tailscale payload, and installed and running on unit `.42`. Adds a **Tailscale** panel to birdUI's
System page so a PLAY can be signed in from the browser. Built 2026-08-13.

**Proven on unit `.42` (fw 1.0.30) 2026-08-13 — INSTALLED AND RUNNING there.**
Live `/srv` patched, `BirdDogWebUI` restarted (pid 311 -> 1752, so it genuinely
reloaded), `/login` + `/dashboard` 200, **System Update form intact**, asset
served, `bd-tailscale-ui.service` enabled and answering over LAN and tailnet.
Patch/unpatch round-trips byte-identically; `/api/status` answers correctly
off real tailscaled 1.102.2 (`tun:false`, `exit_node_capable:false`, so
userspace-networking is confirmed and route advertising is correctly refused);
**`tailscale debug prefs` DOES exist and parses**, which had been flagged as
uncertain; and the gate returns 403 for both a missing and a forged session,
with `device_protected:true` — `.42` DOES have a birdUI password, so the
differential probe has something real to detect.
**Still unproven, and it shipped anyway on Allan's call:** (1) **the browser
actually sending the cookie cross-port** — needs a real birdUI login, which
Claude has no password for; if wrong, writes refuse from a good session and the
gate needs an explicit password field; (2) the interactive login flow — testing
it would log a working tailnet node out, which is how `.42` is reached.
**`GET /settings` unauthenticated 302s to `/dashboard`, NOT to `/login`** — the
gate treats any 3xx as unauthenticated, which is why that works.

**Why it exists:** [birddog play patcher](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/docs/NOTES.md) (`birddog-play-patcher`) deliberately bakes **no auth
key** into the `.fw` (cleartext in a browser download), so every device it makes
came up with Tailscale installed but unauthenticated and could only be finished
over SSH on port 9031. Ships as part of `--with-tailscale`, not a separate flag.

**The live System page is `settings.html`, NOT `system-settings.html`.** Both are
in `/srv/birddog-web-ui/`; `system-settings.html` is older markup the firmware no
longer serves. header.html links `/settings`, and settings.html is the one built
on `layout.html` (`{{template "layout.html" .}}` + `{{define "settings"}}`) like
every current page. Patching the wrong one is a **silent no-op that reads exactly
like a broken patch**. Byte-identical between firmware 1.0.32 and 1.0.34 — **but NOT 1.0.30, which is
what the test unit `.42` actually runs.** 1.0.30's page is 13,377 bytes (vs
17,545) and has **no UI Mode section at all**.

**The anchor trap, caught only by hardware 2026-08-13.** The patch first
anchored on `<!-- End UI Mode -->` — verified unique across 1.0.32 and 1.0.34,
every test green — and that string **does not exist on 1.0.30**, so the patch
refused to apply on the only unit we own. It failed safe, but the feature was
dead. Now anchors on **`</body>`**, which occurs exactly once in every version
checked, and the tests run against BOTH firmware fixtures.
**Generalise it: anchor on structure the document must have, not on a feature a
version might not ship.**

**That page carries the System Update form** — the firmware-upload recovery path
— so a parse failure takes the way back with it. Same mitigations as
[bdcam](https://github.com/stoatworks-labs/bdcam/blob/main/docs/NOTES.md) (`bdcam`)'s UVC tab: tested Go patch, markers, refuse unfamiliar markup,
`.bdts-stock` backup, atomic write, plus installer health check requiring the
**pid to change** (unit is `BirdDogWebUI`; `birddog-web-ui` is the binary).
Inserted block is one empty div + one script tag with **zero template actions**,
asserted by counting `{{`/`}}` and by parsing the patched file with
`html/template`.

**Port map on the device — 8091 was already taken:** 80 birdUI, 8080 BirdDog's
own unauthenticated API, 8090 `bd-cam-api`, **8091 `bd-play`**
([bdplay](https://github.com/stoatworks-labs/bd-play-usb-player/blob/main/docs/NOTES.md) (`bd-play-usb-player`)), so bdts took **8092**. A collision is silent: the second
service fails to bind and its panel just never answers.

**Auth is where this departs from bdcam.** bdcam documents its API as
unauthenticated because `:8080` already is; that does not carry over, because an
open Tailscale API lets a LAN attacker join the box to **their** tailnet for
persistent remote access. Writes are gated on a birdUI session; reads stay open.
- **Cookies are scoped by host, NOT by port**, so the `BirdDogSession` cookie the
  browser already holds is sent to :8092 — no second password. Needs
  `credentials:'include'` **and a concrete `Access-Control-Allow-Origin`**:
  a wildcard origin may not carry credentials, so the browser silently drops the
  cookie and every write looks unauthenticated. **This is the load-bearing
  unverified assumption.**
- The cookie is validated by re-requesting `/settings` from 127.0.0.1 with it.
  **The trap: that proves nothing unless the page is actually protected.** With
  no birdUI password the unauthenticated probe also succeeds, so the check is
  **differential** — probe without the cookie too, and refuse the write if that
  also comes back authenticated, rather than pretending to have gated it.
- Session cookie name `BirdDogSession`, recovered from the web UI binary's string
  table. Routes in there: `/login`, `/logout`, `/auth`, `/dashboard`,
  `/videoset`, `/settings`.

**Unresolved contradiction worth settling on hardware:** [bdplay](https://github.com/stoatworks-labs/bd-play-usb-player/blob/main/docs/NOTES.md) (`bd-play-usb-player`)
records `videoset.html` as parsed **from disk on every request** (verified by
editing and seeing it served); bdcam's commit records it as parsed **at startup**
(the tab only appeared after restarting the right unit). Both are hardware
observations of structurally identical templates. bdts assumes the safe case and
restarts with health check + rollback, which is correct either way.

Settings go through **`tailscale set`, never `tailscale up`** — `up` on a live
node re-runs the login flow and resets every pref not named. Interactive login is
**started detached** and the URL read back from `status --json`'s `AuthURL`,
because `up` blocks until a human finishes. No TUN on this kernel, so the panel
refuses to offer advertise-routes/exit-node rather than showing switches that
fail. Toggle state comes from `tailscale debug prefs` — a debug command, treated
as optional.

**fwbuild wiring is APPLIED but deliberately NOT COMMITTED** in `birddog-re`:
`build.sh` and `payload/update` also carry the bdplay session's uncommitted work,
and committing either file would take theirs under this message
(**cosession shared checkout** (working-practice note, kept in Claude memory)). The packager builds a correct 36 MB
package from the working tree. `docs/fwbuild-wiring.md` documents the blocks.

**Ships with `--with-tailscale`, not behind its own flag** (escape hatch
`--no-tailscale-ui`), because a Tailscale install that still needs SSH to finish
is the problem it solves. The patcher vendors only the BINARY
(`tailscale-ui/dist/`), like bdplay — the source stays in its own public repo so
there is no second copy to drift. Its test asserts the panel is IN the package
and that `build.conf` never ships one half of the pair: **silent absence is this
feature's failure mode** — the device installs perfectly and still wants SSH.

Related: [birddog re](https://github.com/stoatworks-labs/birddog-re/blob/main/docs/NOTES.md) (`birddog-re`), [birddog play patcher](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/docs/NOTES.md) (`birddog-play-patcher`),
[browser pane verification traps](https://github.com/stoatworks-labs/fleet-notes/blob/main/notes/reference_browser_pane_verification_traps.md).
