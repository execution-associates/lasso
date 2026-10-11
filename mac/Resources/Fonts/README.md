# Fonts

The Execution Associates faces, as isb's web app uses them (`isb web/src/fonts.css`): Geist for text, Geist Mono for code, and Archivo, widened, for titles. Each is the variable TTF from Google Fonts' repository (`github.com/google/fonts`, `ofl/geist`, `ofl/geistmono`, `ofl/archivo`), licensed under the SIL Open Font License 1.1. The licence for each is beside it.

`build-app.sh` copies this folder into the bundle's `Contents/Resources/Fonts`, and the app registers the faces for its own process at launch (`BundledFonts.register`). Nothing is installed system-wide.
