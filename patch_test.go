package main

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The System pages the patch is tested against.
//
// Two sources, on purpose:
//
//   * The synthetic pages below ship with the repo. They are written here, not
//     copied from a device: the real templates are BirdDog's copyrighted UI
//     source and this repo is public, so it carries none of it — the same line
//     bd-play-usb-player draws. They reproduce the structure the patch depends
//     on, including the difference that actually bit us.
//   * Any real firmware pages found in testdata/firmware/ (gitignored) are
//     tested too. Drop a device's settings.html in there as
//     settings.<version>.html and it is picked up automatically.
//
// The difference that bit us: the patch first anchored on
// "<!-- End UI Mode -->", which is unique in firmware 1.0.34 and does not exist
// at all in 1.0.30 — the UI Mode section had not been added yet. Every test
// passed and the patch refused to apply on the only unit we own. Hence
// syntheticNoUIMode, and hence anchoring on </body> instead.

const syntheticWithUIMode = `{{template "layout.html" .}}
{{define "settings"}}
<body>
    <div class="div_content_box"><span class="font_gray_color">Password Settings</span></div>
    <div class="div_content_box"><span class="font_gray_color">System Update</span>
        <input type="file" name="update_file" />
        <button {{if .Ready}}disabled{{end}} value="update">Update</button>
    </div>
    <div class="div_content_box"><span class="font_gray_color">Reset System Default</span></div>
    <!-- UI Mode -->
    <div class="div_content_box">
        <input {{if eq .UIMode "dark"}} checked {{end}} type="radio" value="dark" name="uimode" />
    </div>
    <!-- End UI Mode -->
</body>
<script>function pass_view() {}</script>
{{end}}`

// syntheticNoUIMode is the older shape: same page, no UI Mode section, so no
// "<!-- End UI Mode -->" anywhere in it.
const syntheticNoUIMode = `{{template "layout.html" .}}
{{define "settings"}}
<body>
    <div class="div_content_box"><span class="font_gray_color">Password Settings</span></div>
    <div class="div_content_box"><span class="font_gray_color">System Update</span>
        <input type="file" name="update_file" />
        <button value="update">Update</button>
    </div>
    <div class="div_content_box"><span class="font_gray_color">Reset System Default</span></div>
</body>
<script>function pass_view() {}</script>
{{end}}`

// pages returns every System page available to test: the synthetic ones always,
// plus any real firmware dropped into testdata/firmware/.
func pages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{
		"synthetic/with-ui-mode": syntheticWithUIMode,
		"synthetic/no-ui-mode":   syntheticNoUIMode,
	}
	matches, err := filepath.Glob("testdata/firmware/settings.*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("reading %s: %v", m, err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "settings."), ".html")
		out["firmware/"+name] = string(b)
	}
	if len(matches) == 0 {
		t.Log("no real firmware pages in testdata/firmware/ — testing synthetic pages only. " +
			"Copy a device's /srv/birddog-web-ui/settings.html there to test the genuine markup.")
	}
	return out
}

// eachFirmware runs fn against every available System page.
func eachFirmware(t *testing.T, fn func(t *testing.T, src string)) {
	t.Helper()
	for name, src := range pages(t) {
		t.Run(name, func(t *testing.T) { fn(t, src) })
	}
}

// stock is one representative page, for tests that only need a single input.
func stock(t *testing.T) string {
	t.Helper()
	return syntheticWithUIMode
}

func TestAnchorIsUniqueInEveryFirmware(t *testing.T) {
	eachFirmware(t, func(t *testing.T, src string) {
		if n := strings.Count(src, anchor); n != 1 {
			t.Fatalf("anchor %q appears %d times, want exactly 1 — the patch would refuse to apply on this firmware", anchor, n)
		}
	})
}

// The insertion must land inside {{define "settings"}}...{{end}}, or the panel
// would be defined outside the block the page actually renders and would never
// appear — while still parsing cleanly, so nothing would look wrong.
func TestPatchLandsInsideTheDefineBlock(t *testing.T) {
	out, err := PatchSettings(stock(t), tailscaleJS)
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	at := strings.Index(out, patchStart)
	end := strings.LastIndex(out, "{{end}}")
	if at < 0 || end < 0 {
		t.Fatalf("markers missing: patch at %d, {{end}} at %d", at, end)
	}
	if at > end {
		t.Errorf("patch inserted after the closing {{end}}, so it is outside the settings template")
	}
	if body := strings.Index(out, "</body>"); body >= 0 && at > body {
		t.Errorf("patch inserted after </body>")
	}
}

// A patch that changes the number of template actions is a patch that can break
// the parse. Ours adds none at all, and this is the check that keeps it that way.
func TestPatchAddsNoTemplateActions(t *testing.T) {
	src := stock(t)
	out, err := PatchSettings(src, tailscaleJS)
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	for _, tok := range []string{"{{", "}}"} {
		if before, after := strings.Count(src, tok), strings.Count(out, tok); before != after {
			t.Errorf("%q count changed from %d to %d — the patch introduced a template action", tok, before, after)
		}
	}
}

// The test this whole file exists for. birddog-web-ui parses these templates
// with html/template; a parse error does not show up when the file is written,
// only when the service reads it, and it takes the entire web UI down with it —
// including the firmware upload form that is the way back from a bad install.
// So: parse the stock page, parse the patched page, and require both to succeed.
func TestPatchedTemplateStillParses(t *testing.T) {
	eachFirmware(t, func(t *testing.T, src string) {
		if _, err := template.New("page").Parse(src); err != nil {
			t.Fatalf("the stock page does not parse, so this test proves nothing: %v", err)
		}
		out, err := PatchSettings(src, tailscaleJS)
		if err != nil {
			t.Fatalf("PatchSettings: %v", err)
		}
		if _, err := template.New("page").Parse(out); err != nil {
			t.Fatalf("patched System page fails to parse — installing this would take birdUI down: %v", err)
		}
	})
}

// html/template rewrites what it considers script content, and a stray quote or
// a "</script>" inside the asset would break out of the tag. The asset is
// served as a static file rather than inlined precisely to avoid that, and this
// asserts the template edit keeps it that way.
func TestPatchDoesNotInlineTheScript(t *testing.T) {
	out, err := PatchSettings(stock(t), tailscaleJS)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(out, patchStart), strings.Index(out, patchEnd)
	inserted := out[i:j]
	if strings.Contains(inserted, "function") || len(inserted) > 400 {
		t.Errorf("the patch inlined script content into the template (%d bytes); it must only reference /static/", len(inserted))
	}
}

func TestPatchIsIdempotent(t *testing.T) {
	once, err := PatchSettings(stock(t), tailscaleJS)
	if err != nil {
		t.Fatalf("first patch: %v", err)
	}
	twice, err := PatchSettings(once, tailscaleJS)
	if err != nil {
		t.Fatalf("second patch: %v", err)
	}
	if once != twice {
		t.Error("patching twice changed the file again; an installer re-run would double the panel up")
	}
	if n := strings.Count(twice, patchStart); n != 1 {
		t.Errorf("found %d start markers after two patches, want 1", n)
	}
}

func TestUnpatchRestoresByteIdentical(t *testing.T) {
	eachFirmware(t, func(t *testing.T, src string) {
		out, err := PatchSettings(src, tailscaleJS)
		if err != nil {
			t.Fatalf("PatchSettings: %v", err)
		}
		if got := UnpatchSettings(out); got != src {
			t.Errorf("unpatch did not restore the stock file exactly\n--- want %d bytes\n+++ got %d bytes", len(src), len(got))
		}
	})
}

// Repeated cycles are the realistic case: install, uninstall, reinstall.
func TestPatchUnpatchCyclesAreStable(t *testing.T) {
	eachFirmware(t, func(t *testing.T, src string) { patchCycles(t, src) })
}

func patchCycles(t *testing.T, src string) {
	cur := src
	for i := range 5 {
		var err error
		if cur, err = PatchSettings(cur, tailscaleJS); err != nil {
			t.Fatalf("cycle %d patch: %v", i, err)
		}
		cur = UnpatchSettings(cur)
		if cur != src {
			t.Fatalf("cycle %d did not return to stock (%d bytes vs %d)", i, len(cur), len(src))
		}
	}
}

// The important failure mode: a firmware whose System page this patch was not
// written for must be refused, not patched on a guess.
func TestPatchRefusesUnfamiliarMarkup(t *testing.T) {
	cases := map[string]string{
		"anchor missing":    "<html><p>nothing familiar here, and no closing body tag</p>",
		"anchor duplicated": stock(t) + stock(t),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := PatchSettings(src, tailscaleJS); err == nil {
				t.Fatal("expected an error, got none — this would edit a page it does not understand")
			}
		})
	}
}

// An end marker deleted by hand must not cause the rest of the file to be eaten.
func TestUnpatchLeavesTruncatedMarkersAlone(t *testing.T) {
	src := "before\n" + patchStart + "<div></div>\nafter, including the update form"
	if got := UnpatchSettings(src); got != src {
		t.Errorf("unpatch modified a file with no end marker:\n%q", got)
	}
}

func TestAssetVersionTracksContent(t *testing.T) {
	a := assetVersion("one")
	if a != assetVersion("one") {
		t.Error("asset version is not stable for identical content")
	}
	if a == assetVersion("two") {
		t.Error("asset version did not change with content; browsers would serve the old panel from cache")
	}
}

func TestRenderAssetSubstitutesPort(t *testing.T) {
	js := renderAsset(9999)
	if strings.Contains(js, portPlaceholder) {
		t.Error("port placeholder survived rendering; the panel would fail to parse")
	}
	if !strings.Contains(js, "9999") {
		t.Error("configured port did not reach the script")
	}
}

// The embedded script must never contain the marker sequences, or unpatching
// would cut the file at the wrong place.
func TestAssetDoesNotContainMarkers(t *testing.T) {
	for _, m := range []string{patchStart, patchEnd} {
		if strings.Contains(tailscaleJS, m) {
			t.Errorf("embedded script contains the marker %q", m)
		}
	}
}

func TestApplyAndRemoveOnDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := stock(t)
	u := UIPaths{Dir: dir}
	if err := os.WriteFile(u.Settings(), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ApplyPatch(u, 8092); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	patched, _ := os.ReadFile(u.Settings())
	if !IsPatched(string(patched)) {
		t.Fatal("file on disk is not patched")
	}
	if _, err := os.Stat(u.Asset()); err != nil {
		t.Fatalf("asset not written: %v", err)
	}
	backup, err := os.ReadFile(u.Backup())
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if string(backup) != src {
		t.Error("backup is not the stock file")
	}

	// A second install must not overwrite the backup with an already-patched
	// copy — that copy is the only thing known to be stock.
	if err := ApplyPatch(u, 8092); err != nil && !isAlreadyPatched(err) {
		t.Fatalf("second ApplyPatch: %v", err)
	}
	backup2, _ := os.ReadFile(u.Backup())
	if string(backup2) != src {
		t.Error("backup was overwritten with patched content by the second install")
	}

	if err := RemovePatch(u); err != nil {
		t.Fatalf("RemovePatch: %v", err)
	}
	after, _ := os.ReadFile(u.Settings())
	if string(after) != src {
		t.Error("RemovePatch did not restore the stock file")
	}
	if _, err := os.Stat(u.Asset()); !os.IsNotExist(err) {
		t.Error("asset still present after removal")
	}
}

func TestRestoreBackup(t *testing.T) {
	dir := t.TempDir()
	u := UIPaths{Dir: dir}
	src := stock(t)
	if err := os.WriteFile(u.Settings(), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPatch(u, 8092); err != nil {
		t.Fatal(err)
	}
	// Simulate the disaster case: the template is now unparseable.
	if err := os.WriteFile(u.Settings(), []byte("{{ this will not parse"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(u); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _ := os.ReadFile(u.Settings())
	if string(got) != src {
		t.Error("rollback did not return the stock System page — the firmware upload form would stay broken")
	}
}

func isAlreadyPatched(err error) bool { return err == errAlreadyPatched }
