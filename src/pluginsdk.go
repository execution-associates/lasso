package main

import (
	"bytes"
	"embed"
	"net/http"
	"path"
	"time"
)

// The plugin SDK: a bridge client and base stylesheet a plugin page loads
// from /plugins/_sdk/ to wear lasso's look (pluginsdk/lasso.js applies the
// theme the bridge pushes; lasso.css styles a page with those tokens). It is
// lasso's own code, so it ships in the binary rather than in any plugin, and
// "_sdk" can never be a plugin's name (pluginNameRE starts with a letter).
//
//go:embed pluginsdk/lasso.js pluginsdk/lasso.css
var pluginSDKFS embed.FS

var pluginSDKTypes = map[string]string{
	"lasso.js":  "text/javascript; charset=utf-8",
	"lasso.css": "text/css; charset=utf-8",
}

// pluginSDKModTime is the process start: the files change only with the
// binary, and serveFiles' no-cache makes every load revalidate anyway.
var pluginSDKModTime = time.Now()

// servePluginSDK answers /plugins/_sdk/<file>. serveFiles has already set the
// sandbox CSP, nosniff and no-cache headers every /plugins/ response carries.
func servePluginSDK(w http.ResponseWriter, r *http.Request, file string) {
	ct, ok := pluginSDKTypes[file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := pluginSDKFS.ReadFile(path.Join("pluginsdk", file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, file, pluginSDKModTime, bytes.NewReader(b))
}
