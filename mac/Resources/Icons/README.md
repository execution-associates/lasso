# Icons

The app's icons: Lucide's (lucide.dev), the set lasso's web app draws through `lucide-react`, one SVG per icon in lucide-static's form (24 px grid, `stroke="currentColor"`, stroke 2). `LICENSE` is Lucide's: ISC, with the MIT notice for the icons derived from Feather. `build-app.sh` copies this folder into the bundle's `Contents/Resources/Icons`, and `Theme/Lucide.swift` draws them as template images.

`mac/Scripts/lucide-icons.sh` (on titan) writes every `.svg` here and `LICENSE`: it reads the icon data of the lucide-react that `src/web/bun.lock` pins (1.17.0) inside this worktree's dev container, and builds the SVGs from it, accepting only plain shapes. The names are the cases of `enum Lucide`; add one there and run the script. Don't edit the SVGs by hand.
