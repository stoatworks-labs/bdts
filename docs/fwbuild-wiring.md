# Wiring bdts into `birddog-re/tools/fwbuild`

The panel installs **as part of the Tailscale payload**, not behind a flag of
its own. A Tailscale install that still needs SSH to finish is the problem this
solves, so making the fix optional would leave the default broken. There is an
escape hatch (`--no-tailscale-ui`) for iterating on a package quickly.

This is presented as blocks to insert rather than a `git diff` on purpose:
`build.sh` and `payload/update` are edited often and by more than one person, and
a context diff written against a moving file fails to apply for reasons that
look like a broken patch.

## 1. `tools/fwbuild/build.sh`

**Near the other defaults**, beside `WITH_TAILSCALE=1`:

```bash
WITH_TAILSCALE_UI=1
```

**In `usage()`**, under the Tailscale lines:

```
  --no-tailscale-ui   install Tailscale without the birdUI System-page panel.
                      The panel is what lets a device be signed in from the
                      browser; without it, finishing the install needs SSH.
```

**In the argument loop:**

```bash
    --no-tailscale-ui) WITH_TAILSCALE_UI=0; shift ;;
```

**In the `build.conf` heredoc**, after `WITH_TAILSCALE=$WITH_TAILSCALE`:

```bash
WITH_TAILSCALE_UI=$WITH_TAILSCALE_UI
```

**Inside the existing `if [ "$WITH_TAILSCALE" = 1 ]` block**, after the
`aarch64` check on `tailscaled`:

```bash
  # The birdUI panel. Its own repo, like bdcam and bd-play-usb-player; expected
  # as a sibling checkout, override with BDTS=/path/to/bdts-linux-arm64.
  if [ "$WITH_TAILSCALE_UI" = 1 ]; then
    BDTS="${BDTS:-$REPO/../bdts/dist/bdts-linux-arm64}"
    [ -f "$BDTS" ] || {
      echo "error: bdts binary not found at $BDTS" >&2
      echo "  clone github.com/stoatworks-labs/bdts beside this repo and run its build.sh," >&2
      echo "  or set BDTS=/path/to/bdts-linux-arm64, or pass --no-tailscale-ui" >&2
      exit 1; }
    file "$BDTS" | grep -q aarch64 || { echo "error: bdts is not aarch64" >&2; exit 1; }
    mkdir -p "$STAGE/userdata/bd-tailscale-ui"
    cp "$BDTS" "$STAGE/userdata/bd-tailscale-ui/bdts"
    chmod 755 "$STAGE/userdata/bd-tailscale-ui/bdts"
    echo "including the birdUI Tailscale panel"
  fi
```

## 2. `tools/fwbuild/payload/update`

**With the other defaults**, beside `WITH_TAILSCALE="${WITH_TAILSCALE:-0}"`:

```bash
WITH_TAILSCALE_UI="${WITH_TAILSCALE_UI:-0}"
```

**At the end of the tailscale block**, immediately after
`log "tailscale unit written (tun args: '${TUNARG:-none}')"`, so the panel is
only installed when Tailscale itself actually was (that branch is skipped when
the disk-space check fails):

```bash
    TS_INSTALLED=1
```

**Then a new section, after the whole tailscale block closes:**

```bash
# --------------------------------------------------------- tailscale birdUI panel
# Signing Tailscale in from the web UI instead of over SSH.
#
# This patches /srv/birddog-web-ui/settings.html — the System page, and NOT
# system-settings.html, which is older markup this firmware does not serve. That
# page also carries the System Update form, so a bad edit takes the firmware
# upload path down with it. The patch itself lives in bdts, where it is unit
# tested against the real stock file, refuses unfamiliar markup and is exactly
# reversible; the health check and rollback below are the second line.
if [ "${TS_INSTALLED:-0}" = 1 ] && [ "$WITH_TAILSCALE_UI" = 1 ] && [ -f "$SDIR/userdata/bd-tailscale-ui/bdts" ]; then
  log "installing the birdUI Tailscale panel"
  mkdir -p /userdata/bd-tailscale-ui
  cp -f "$SDIR/userdata/bd-tailscale-ui/bdts" /userdata/bd-tailscale-ui/
  chmod 755 /userdata/bd-tailscale-ui/bdts
  chown -R root:root /userdata/bd-tailscale-ui

  cat > /etc/systemd/system/bd-tailscale-ui.service <<'EOF'
[Unit]
Description=Tailscale settings panel for birdUI (custom)
After=network.target BirdDogWebUI.service bd-tailscaled.service
Wants=bd-tailscaled.service

[Service]
Type=simple
ExecStart=/userdata/bd-tailscale-ui/bdts --serve :8092
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  # 8092 because 8080 is BirdDog's API, 8090 is bd-cam-api and 8091 is bd-play.
  # A collision here is silent: the second service to start fails to bind and
  # its panel simply never answers.

  WEBUI_UNIT=BirdDogWebUI
  if ! systemctl cat "$WEBUI_UNIT" >/dev/null 2>&1; then
    log "WARNING: no $WEBUI_UNIT unit on this firmware — skipping the Tailscale panel"
  elif /userdata/bd-tailscale-ui/bdts --patch-ui --ui-dir /srv/birddog-web-ui --api-port 8092 >> /tmp/bd-custom-install.log 2>&1; then
    PID_BEFORE=$(pidof birddog-web-ui 2>/dev/null | awk '{print $1}')
    log "System page patched; restarting $WEBUI_UNIT (was pid ${PID_BEFORE:-none})"
    systemctl restart "$WEBUI_UNIT" 2>/dev/null
    sleep 5
    PID_AFTER=$(pidof birddog-web-ui 2>/dev/null | awk '{print $1}')
    CODE=$(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1/login 2>/dev/null || echo 000)
    # Same trap as the UVC tab: an unchanged pid means the template was never
    # re-read, so an HTTP 200 is answered by the process that never reloaded.
    if [ -n "$PID_AFTER" ] && [ "$PID_AFTER" != "$PID_BEFORE" ] && { [ "$CODE" = 200 ] || [ "$CODE" = 302 ] || [ "$CODE" = 301 ]; }; then
      log "web UI healthy after patch (pid ${PID_BEFORE:-none} -> $PID_AFTER, HTTP $CODE) — Tailscale panel is live"
    else
      log "WARNING: web UI unhealthy after patching (pid ${PID_BEFORE:-none} -> ${PID_AFTER:-none}, HTTP $CODE) — ROLLING BACK"
      /userdata/bd-tailscale-ui/bdts --unpatch-ui --ui-dir /srv/birddog-web-ui >> /tmp/bd-custom-install.log 2>&1
      systemctl restart "$WEBUI_UNIT" 2>/dev/null
      sleep 5
      log "after rollback: HTTP $(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1/login 2>/dev/null || echo 000); stock template also at /srv/birddog-web-ui/settings.html.bdts-stock"
    fi
  else
    log "WARNING: could not patch the System page — left untouched"
  fi
fi
```

**In the service-enabling section**, beside the existing `bd-tailscaled` block:

```bash
if [ -f /etc/systemd/system/bd-tailscale-ui.service ]; then
  systemctl enable bd-tailscale-ui.service 2>/dev/null
  systemctl restart bd-tailscale-ui.service 2>/dev/null && log "tailscale panel started" \
    || log "WARNING: the tailscale panel failed to start — check 'journalctl -u bd-tailscale-ui'"
fi
```

**Finally**, the closing message currently reads:

```
log "tailscale is installed but NOT authenticated — ssh in on port 9031 and run:"
log "  /userdata/tailscale/tailscale --socket=/run/bd-tailscaled.sock up"
```

Which is exactly what stops being true. Make it conditional:

```bash
if [ "${TS_INSTALLED:-0}" = 1 ]; then
  if [ "$WITH_TAILSCALE_UI" = 1 ] && [ -f /etc/systemd/system/bd-tailscale-ui.service ]; then
    log "tailscale is installed but NOT authenticated — sign in from the System page of the web UI,"
    log "or ssh in on port 9031 and run:"
  else
    log "tailscale is installed but NOT authenticated — ssh in on port 9031 and run:"
  fi
  log "  /userdata/tailscale/tailscale --socket=/run/bd-tailscaled.sock up"
fi
```

## Two things to decide when landing this

**The double restart.** With `--with-cam-ui` the installer already patches
`videoset.html` and restarts `BirdDogWebUI`; this adds a second patch and a
second restart of the same service. Harmless but wasteful, and it doubles the
window in which the web UI is down. Worth folding both into one patch-then-check
pass — at which point the rollback has to know which of the two patches to
revert, so it is a real refactor rather than a tidy-up. Two restarts is the
right first version.

**The public patcher.** `birddog-play-patcher` ships Tailscale and tells people
to SSH in to finish, which this removes. Because bdts is its own public repo it
can be shipped the way bdplay is — the patcher syncs `dist/` binaries and links
to the source, with no second copy to drift. It adds ~6.3 MB to a package,
well inside the 25 MiB per-file Workers limit. The BSD-3 notice for Tailscale's
own binaries is unchanged; bdts is MIT and needs a line of its own.
