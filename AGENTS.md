# AGENTS.md — bdts

Tailscale authentication and settings inside birdUI, on the **BirdDog PLAY**
(Rockchip RK3328, quad A53, Debian 10 aarch64). Go, cross-compiled for the
device, no cgo.

Start with [`README.md`](README.md) — it carries the design, the security
argument and the hardware verification that is still outstanding. This file is
the operating rules.

## Where this sits

`bdts` is the canonical, standalone home of this program, alongside `bdcam` and
`bd-play-usb-player` as sibling checkouts under `~/Projects`. The reverse
engineering that justifies every claim in the README lives in
`~/Projects/birddog-re` — **local-only, no remote, and it must stay that way**:
its history contains a recovered vendor AES key and a decrypted firmware blob.
Do not push `birddog-re` anywhere, and do not copy key material or vendor
firmware into this repo.

`birddog-re`'s `tools/fwbuild` finds this repo's binary at
`../bdts/dist/bdts-linux-arm64` (override with `BDTS=`).

## Hard rules

1. **Never patch `system-settings.html`.** It is older markup that the current
   firmware does not serve. The live System page is **`settings.html`**, linked
   from the nav as `/settings` and built on `layout.html` like every other
   current page. Patching the wrong file is a silent no-op that reads exactly
   like a broken patch, and costs an hour to spot.
2. **The template edit must never contain a template action.** `settings.html`
   carries the System Update form; a parse failure takes the firmware upload
   page down, and that is the way back from a bad install. The inserted block is
   one empty `<div>` and one `<script>` tag, and `TestPatchAddsNoTemplateActions`
   plus `TestPatchedTemplateStillParses` are what keep it that way. Put UI
   changes in `web/bdts-tailscale.js`, which is served from `/static/` and is
   outside the template system entirely.
3. **Keep the auth gate differential.** Checking that a session cookie is
   accepted proves nothing on a device with no birdUI password, because the
   unauthenticated probe succeeds too. `deviceProtected` is what stops this
   being theatre — do not "simplify" it away, and do not make any failure path
   fail open. See `TestGateRefusesWhenDeviceHasNoPassword`.
4. **CORS must echo the origin, never `*`.** The panel sends the birdUI session
   cookie cross-origin, and a wildcard origin is not permitted to carry
   credentials — the browser drops the cookie and every write looks
   unauthenticated, with nothing in any log to say why.
5. **Settings changes use `tailscale set`, never `tailscale up`.** `up` on a
   live node re-runs the login flow and resets every pref not named on the
   command line.
6. **Build only through `./build.sh`**, or with `GOOS=linux` set. This is
   Linux-only code.
7. **Never commit BirdDog's firmware HTML.** This repo is public; those
   templates are their copyrighted UI source. `testdata/firmware/` is gitignored
   and holds real pages for local testing only, and the fixtures that ship are
   written by hand in `patch_test.go`. Same line `bd-play-usb-player` draws — it
   carries no vendor markup either.

## Toolchain

```bash
./build.sh            # -> dist/bdts-linux-arm64 (CGO_ENABLED=0, static)
go test ./...         # patch, gate and CLI behaviour, all on the host
GOOS=linux GOARCH=arm64 go vet ./...
```

No zig and no cgo: unlike `bdkvm` and `bdcam`, this dlopens nothing — it drives
the `tailscale` CLI and `systemctl` as subprocesses.

## Layout

```
main.go        CLI: --patch-ui, --unpatch-ui, --restore-ui, --serve, --status
patch.go       the settings.html patch; markers, anchor, backup, atomic writes
auth.go        the birdUI session gate, including the differential probe
serve.go       the API the panel calls, and CORS with credentials
ts.go          the tailscale CLI wrapper: status, up, down, logout, set, prefs
web/…          the panel itself, embedded and written to the web UI's static dir
testdata/      gitignored; real firmware pages for local testing, never committed
```

## Testing

Everything runs on the host. The CLI is injected as a `Runner`/`Starter` pair so
no test shells out, and the stock web UI is faked with an `httptest` server that
serves the System page or the login page depending on the cookie.

The patch tests run against two hand-written pages that ship with the repo, plus
**every real `settings.html` in the gitignored `testdata/firmware/`**. Drop a
device's page in there as `settings.<version>.html` and it is picked up
automatically — do that whenever you meet a new firmware.

This matters more than it looks. The patch originally anchored on
`<!-- End UI Mode -->`, unique in 1.0.34 and **absent from 1.0.30**, which is
what the test unit runs. Every test was green and the patch refused to apply on
real hardware. `syntheticNoUIMode` exists to keep that case in the suite for
anyone without a device. If `TestAnchorIsUniqueInEveryFirmware` fails on a new
page, that is the suite doing its job — find an anchor that holds everywhere,
do not special-case a version.

## Not yet true

**Nothing here has run on a PLAY.** The README's "Verifying on hardware" list is
the outstanding work. The load-bearing unverified assumption is that the
`BirdDogSession` cookie reaches this daemon's port — cookies are scoped by host
and not by port, so it should, but "should" is not "did". If it does not, the
gate needs an explicit password field and the README's security argument needs
rewriting, not patching.

## Notes

`docs/NOTES.md` carries this repo's working notes — current status, decisions
already made, and the traps that have actually bitten. Read it before changing
anything non-obvious. Cross-cutting fleet knowledge lives in
[fleet-notes](https://github.com/stoatworks-labs/fleet-notes).
