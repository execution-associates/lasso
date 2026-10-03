---
title: Theming
description: How lasso themes the terminal, the interface and your agent CLIs, from bundled and Omarchy themes to appearance modes, backgrounds, plugin fonts and fleet-wide sync.
order: 34
nav_title: Theming
---

lasso keeps three things looking the same: the terminals, the interface around them (the sidebar, the dialogs, the footer), and the agent CLIs running inside the panes. A theme change made in lasso reaches herdr's own config, every browser, and every host you have selected for sync.

Everything here is in **Settings → Themes**, which has five groups: **Appearance**, **Background**, **Typography**, **Install a theme** and **Fleet sync**.

## Two kinds of theme choice

There are two separate choices, and it helps to keep them apart:

- **The herdr theme** is the theme named in herdr's `config.toml`. herdr's own TUI uses it, and so do lasso's terminals. It is shared with herdr: change it in lasso and herdr repaints; change it in herdr and lasso follows.
- **The appearance** decides what lasso's interface wears: herdr's theme, or a palette you name per light and dark scheme. It is lasso's own setting, stored on the lasso server, so every browser on the same lasso follows it within a moment and with no reload.

When the appearance names a palette, that palette also becomes the theme lasso writes to herdr and the fleet, so the terminals, the agents and the interface all match. See [Appearance](#appearance-and-palettes).

## Themes lasso knows

The theme pickers group themes by where they come from, each split into Dark and Light.

**Brand.** Two themes drawn from Execution Associates' sites:

- **Execution Associates** (`execution-associates`, dark): dusk violet with a peach accent. This is the default when herdr has no theme configured, and the fallback for a name lasso does not recognise.
- **Orange County AI** (`ocai`, light): navy ink and a sun-orange accent on cream.

**Herdr.** Palettes matching herdr's own built-in themes:

| Dark | Light |
| --- | --- |
| Catppuccin, Tokyo Night, Dracula, Nord, Gruvbox, One Dark, Solarized, Kanagawa, Rosé Pine, Vesper, Terminal | Catppuccin Latte, Tokyo Night Day, Gruvbox Light, One Light, Solarized Light, Kanagawa Lotus, Rosé Pine Dawn |

**Omarchy.** The official [Omarchy](https://github.com/basecamp/omarchy) themes, palettes and wallpapers both, bundled into the binary so they work offline. **Retro 82** (`retro-82`, dark navy with amber and teal, from [OldJobobo's Omarchy Retro 82](https://github.com/OldJobobo/omarchy-retro-82-theme)) is listed here. Where an official Omarchy theme shares a name with one of lasso's own palettes (Catppuccin, Nord, and so on), lasso's palette is used and the Omarchy theme contributes its wallpapers.

**Omarchy · Installed.** Community Omarchy themes you installed from a git URL (below).

**Plugin.** Themes contributed by enabled [plugins](../plugins/index.md), labelled with the plugin's name. A plugin theme never replaces an existing name: a built-in, official or installed theme with the same name wins, and the plugin listing says the theme was skipped.

### Installing an Omarchy theme

**Settings → Themes → Install a theme** takes the git URL of an Omarchy theme repository (for example `https://github.com/user/omarchy-<name>-theme`). lasso clones it on lasso's machine and adds it to the pickers. Only `https://` clones are allowed, git hooks are disabled, and only the palette (`colors.toml`, or `alacritty.toml` for a theme without one) and the background images are kept; nothing from the repository is executed. Installed themes live in `~/.lasso/omarchy/themes/`. There is no uninstall button: delete the directory yourself.

On a machine that runs Omarchy, lasso also offers the wallpapers Omarchy keeps for a theme (`~/.config/omarchy/backgrounds/<theme>`, `~/.config/omarchy/themes/<theme>/backgrounds`, `~/.local/share/omarchy/themes/<theme>/backgrounds`, `/usr/share/omarchy/themes/<theme>/backgrounds`).

## The herdr theme

**Settings → Themes → Appearance → Herdr theme (shared)** sets `[theme].name` in herdr's `config.toml`. herdr and every lasso terminal repaint live; there is nothing to restart. lasso then pushes the theme to the hosts and agent CLIs selected under [Fleet sync](#fleet-sync).

This picker is disabled while an appearance palette is in force, because the palette is then the theme the fleet wears. That includes a fresh lasso, whose default appearance (System, with a palette named for each scheme) puts a palette in force. Switch Appearance to **Herdr**, or set the palette to **Nothing** (under System, both palettes), to pick herdr's theme here.

### The `-theme` flag

By default (`-theme auto`) lasso reads the theme from herdr's `config.toml` and follows it live. `lasso serve -theme <name>` forces one theme for this lasso's terminals instead; `lasso serve -h` lists the built-in names. While the flag is set, the terminals do not follow theme changes, and Settings says so.

### How lasso writes herdr's config

herdr accepts only its own built-in theme names, and rejects anything else (`herdr config check` reports an error and the TUI falls back to Catppuccin). So for a theme only lasso knows, such as Retro 82, a brand theme or an Omarchy theme, lasso writes it the way herdr supports: a herdr base theme in `[theme].name`, plus a `[theme.custom]` block that reproduces the palette on top of it. Every line lasso generates is tagged with a comment:

```toml
[theme]
name = "vesper"
# lasso-theme = "retro-82"
# lasso-theme-base = "vesper"

[theme.custom]
accent = "#…" # lasso-theme-token
panel_bg = "#…" # lasso-theme-token
```

The tags are what make this reversible:

- Switching to another theme removes exactly the tagged lines and leaves everything you wrote yourself.
- A token you set yourself in `[theme.custom]` is never overwritten or duplicated.
- If you re-theme in herdr directly (which changes `[theme].name` and knows nothing about the block), lasso notices that the name moved off the recorded base and removes the stranded lines, so herdr does not paint your new theme with the old theme's colours.
- A `[theme].name` you wrote yourself is left as you wrote it, even a name lasso does not recognise.

lasso finds `config.toml` the way herdr does: `$HERDR_CONFIG_PATH`, else `$XDG_CONFIG_HOME/herdr/config.toml`, else `~/.config/herdr/config.toml`, resolved from each host's own environment.

## Appearance and palettes

**Settings → Themes → Appearance** offers four modes:

| Mode | What the interface wears |
| --- | --- |
| **Herdr** | herdr's own theme. No palette is involved, and the herdr theme picker decides everything. |
| **System** | Each device's OS light/dark preference, with the palette named for that scheme. The default. |
| **Light** | Always the light scheme and its palette. |
| **Dark** | Always the dark scheme and its palette. |

Outside **Herdr** mode, **Palette** names a theme for each scheme in force (**light scheme**, **dark scheme**; System shows both). Each picker offers only themes of that lightness. The defaults are **Orange County AI** for light and **Execution Associates** for dark. Choose **Nothing (flat light)** or **Nothing (flat dark)** for lasso's plain monochrome interface, which leaves the terminals on herdr's shared theme.

The mode and the palettes are stored on the lasso server, so picking Dark on your phone repaints the desktop too. Only System's answer is per device: two screens whose OSes disagree each show their own scheme.

When you change the mode or a palette, lasso writes the palette you now see to herdr's `config.toml` and pushes it to the fleet, so the agents in your terminals match your screen. Under System, the last device to act decides what the fleet wears. While a palette is in force, lasso does not adopt a theme change made directly in herdr (its theme popup or a hand edit of `config.toml`); it logs it and keeps the fleet on the palette.

## Backgrounds

Any theme can wear a background image behind the terminals and the interface. **Settings → Themes → Background** offers, for the theme currently on screen:

- **None** (a flat canvas);
- the images lasso bundles: 27 Retro 82 stills, and each brand theme's own site art;
- the wallpapers an Omarchy theme ships (bundled or installed);
- pictures you add by hand: an image URL or an absolute path on lasso's machine (**Add**), or a file from your device (**Upload**). Hand-added pictures are offered for every theme, and only these can be forgotten.

Below the gallery:

- **Palette shading** paints two faint washes of the theme's own colours across the canvas, giving a flat theme some depth with no image. On by default.
- **Image dimming** sets how much of the theme's background colour washes over the picture, to keep text readable. The default is 70%; **Reset to default** restores it.

The picture, shading and dimming are saved **per theme on the lasso server**: every browser on the same lasso shows the same backdrop, a change in one repaints the others within a moment, and switching themes restores each theme's own choices. Nothing about backgrounds is written to herdr's config or to other hosts.

A theme nobody has chosen a background for uses its default: Retro 82 wears its Dusk Guardian still, Execution Associates its golden-hour coast, Orange County AI its Laguna cliffs, and every other theme no image. All of them start with shading on and dimming at 70%.

The terminals always wear the backdrop. The interface wears it too when it is following a palette (Herdr mode, or a named palette); the flat **Nothing** interface stays flat, and Settings says so.

When a backdrop is in force, lasso also adjusts the colours it writes into Claude Code's and Oh My Pi's theme files so their text stays readable against the darkest or lightest point the image could reach at your dimming level. Only text colours that would fail are changed; a theme that already reads well keeps its own colours.

## Typography

**Settings → Themes → Typography** lets you replace lasso's fonts with fonts that enabled plugins provide. With no such plugin, it says so. Each slot can be set independently, or left on **lasso default**:

| Slot | Used for |
| --- | --- |
| **Interface** | Body and interface text |
| **Display** | Large headings |
| **Labels** | Small-caps labels |
| **Code** | Code, the file viewer and diffs |
| **Terminal** | Every terminal. Icon glyphs still come from the bundled Nerd Font. |

The choices are stored on the lasso server. If the plugin providing a chosen font is disabled, lasso's default applies until it is enabled again. To write a plugin that ships fonts, see [Writing a plugin](../plugins/authoring.md).

## Fleet sync

A theme change is pushed to every reachable host in parallel, not only the one this tab is on, since an agent on any host can be on screen at any moment. Each host gets:

- the same `[theme]` section (and generated block) in the `config.toml` its own herdr reads, followed by a nudge for its herdr to reload;
- the agent CLIs' own theme files, so agents render in step with herdr:

| CLI | What lasso writes |
| --- | --- |
| Claude Code | `~/.claude/themes/herdr.json`, selected in its `settings.json` |
| OpenCode | `~/.config/opencode/themes/herdr.json`, selected in `tui.json` |
| Oh My Pi | `~/.omp/agent/themes/herdr.json`, selected for both its light and dark slots (a running omp picks it up live) |
| Ghostty | `~/.config/ghostty/themes/herdr` |

**Reachable over SSH is the only requirement.** A theme write is just files, so a host whose herdr lasso cannot drive (a different protocol version, or herdr stopped) is still written over a files-only SSH connection; only the reload nudge is skipped, and that herdr repaints when it next restarts. A host that was asleep or offline when the theme changed catches up on its own: whenever lasso next reaches it, it compares what it last wrote there with the current theme and pushes if they differ.

**Settings → Themes → Fleet sync** controls this:

- **Sync agent themes (Claude Code, OpenCode, Oh My Pi)** turns the agent CLI files on or off everywhere. On by default.
- **Sync now** pushes the current theme to every reachable host immediately.
- **Sync theme to hosts** lists this machine and every host, each with a checkbox. An unchecked host keeps its own theme: lasso writes neither its herdr `config.toml` nor any agent theme file there. Checking it again pushes the current theme to it right away if it is reachable.
