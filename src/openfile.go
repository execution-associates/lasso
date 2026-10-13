package main

// open_file — an agent shows its human a file, in lasso's sidebar file viewer.
//
// "I wrote the design doc" is only half an answer when the human then has to go
// and find it. This lets the agent put it on their screen: the tool checks the
// file is really there (on the host it names, through the same backend
// /api/file reads with), then pushes a one-shot `open-file` SSE event to every
// connected tab. The browser decides what to do with it — only a VISIBLE tab
// acts, and an editor holding unsaved edits is never replaced (see
// src/web/src/lib/open-file.ts) — so the server's part is to validate, fan out,
// and report honestly how many tabs it reached.
//
// `lasso open <path>` is the same call from a shell, and the only way a
// RELATIVE path works: the MCP server runs in lasso's process and has no idea
// what the caller's working directory is, so the tool refuses one and the CLI
// resolves it before calling.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const openFileDescription = "Open a file in front of the human. `app` (required) picks ONE place: \"web\" — the lasso sidebar file viewer (the Files panel beside their terminal) of every lasso browser tab they have visible; \"mac\" — a window of its own in the Lasso Mac app, on the Mac they are at, that renders it (markdown, HTML, images, video, audio, PDF, Office documents) and follows the file as it changes; to show it in both, call twice. Use it to SHOW them a file: a doc or plan you just wrote when they ask \"open it for me\", a file you want them to review, the place a bug lives. Pass an absolute path (or one starting with ~/, expanded against the target host's home); a relative path is refused because the server cannot see your working directory — `lasso open <path>` in a shell resolves one for you. `host` defaults to YOUR OWN host (the box lasso runs on when your credential names none), which is where your files are; pass it only for a file on another machine. `line` scrolls the web editor to that 1-based line and selects it (a markdown file then opens in its raw editor instead of the rendered preview). A DIRECTORY opens the web sidebar's file tree rooted there rather than the viewer; the Mac app opens nothing for one. The file must exist: a missing path is an error, so write it first. Pass your $HERDR_PANE_ID as pane_id so the human is told which agent opened it. Check `delivered` in the reply: it is the number of lasso tabs or Mac apps the request reached, and 0 means the app you picked is not open, so the human did NOT see it — do not tell them it is on their screen. A tab that is hidden (a phone in a pocket, a background browser tab) or a Mac nobody is using receives it and ignores it, and a viewer holding the human's unsaved edits is not replaced — they get a prompt offering to open it instead."

type openFileIn struct {
	Path   string `json:"path" jsonschema:"Absolute path of the file (or directory) to open, or one starting with ~/ (expanded against the target host's home). Relative paths are refused — the server cannot see your working directory; use \"lasso open <path>\" from a shell for one."`
	Host   string `json:"host,omitempty" jsonschema:"Host the file lives on; omit to target your OWN host — the host your credential was issued for, or the box lasso runs on when it is not host-scoped."`
	Line   int    `json:"line,omitempty" jsonschema:"1-based line to scroll to and select in the editor. Optional; ignored for a directory and for images, PDFs, videos and audio."`
	PaneID string `json:"pane_id,omitempty" jsonschema:"Your own herdr pane id — the value of $HERDR_PANE_ID in your shell. Used only to tell the human which agent opened the file; without it they are told \"an agent\"."`
	App    string `json:"app" jsonschema:"Where to open it, one of the two: \"web\" (the lasso web app's sidebar file viewer) or \"mac\" (a window of its own in the Lasso Mac app)."`
}

type openFileOut struct {
	// Delivered is the number of streams of the chosen app the request was
	// pushed to (Web + Mac, one of them always 0). 0 means nobody saw it.
	Delivered int    `json:"delivered"`
	Web       int    `json:"web"`  // web app tabs it reached
	Mac       int    `json:"mac"`  // Mac apps it reached
	Path      string `json:"path"` // the resolved absolute path that was opened
	Host      string `json:"host"` // the host it was opened on
	Dir       bool   `json:"dir,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// openFileEvent is the `open-file` SSE payload the browser acts on.
type openFileEvent struct {
	Path string `json:"path"`
	Host string `json:"host"`
	Line int    `json:"line,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
	From string `json:"from"`
	// App is the app it was opened for, "web" or "mac". Each app's stream only
	// gets events meant for it; this lets an app that did not name itself
	// (a Mac app from before `?app=mac`) still tell.
	App string `json:"app"`
}

// openFileNobody is the reply's detail when nothing the event was meant for is
// connected — the one outcome an agent must not report as success.
func openFileNobody(app string) string {
	switch app {
	case eventAppWeb:
		return "no lasso web tab is open, so nobody saw it"
	case eventAppMac:
		return "no Lasso Mac app is connected, so nobody saw it"
	}
	return "nobody saw it"
}

// openFileApp normalizes the tool's required `app`: "web" or "mac".
func openFileApp(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "web":
		return eventAppWeb, nil
	case "mac":
		return eventAppMac, nil
	}
	if strings.TrimSpace(s) == "" {
		return "", errors.New("app is required: \"web\" (the lasso web app's sidebar file viewer) or \"mac\" (a window in the Lasso Mac app); call twice to show it in both")
	}
	return "", fmt.Errorf("app must be \"web\" or \"mac\" (got %q)", s)
}

// openFileBackend resolves the host whose filesystem the path is checked on.
// It is namedHostBackend — the resolver /api/file and the viewer itself use —
// so a file this tool accepts is one the browser can then actually load. A var
// so tests can stand a fake fleet in.
var openFileBackend = namedHostBackend

// openFileBroadcast hands the event to every connected stream of the app it is
// for (ev.App) and reports how many web tabs and Mac apps took
// it; a no-op answering 0 before the server is up (tests, CLI paths).
var openFileBroadcast = func(ev openFileEvent) (web, mac int) {
	if srvHub == nil {
		return 0, 0
	}
	if ev.App == eventAppWeb {
		web = srvHub.broadcastTo("open-file", ev, eventAppWeb)
	}
	if ev.App == eventAppMac {
		mac = srvHub.broadcastTo("open-file", ev, eventAppMac)
	}
	return web, mac
}

func openFileTool(ctx context.Context, req *mcp.CallToolRequest, in openFileIn) (*mcp.CallToolResult, openFileOut, error) {
	p := strings.TrimSpace(in.Path)
	if p == "" {
		return nil, openFileOut{}, fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(p) && p != "~" && !strings.HasPrefix(p, "~/") {
		return nil, openFileOut{}, fmt.Errorf("path %q is relative: pass an absolute path (or one starting with ~/) — the server cannot see your working directory. From a shell, `lasso open -app web %s` resolves it for you", p, p)
	}
	app, err := openFileApp(in.App)
	if err != nil {
		return nil, openFileOut{}, err
	}
	if in.Line < 0 {
		return nil, openFileOut{}, fmt.Errorf("line must be a positive, 1-based line number (got %d)", in.Line)
	}
	cs := callerFrom(req)
	host := cs.hostOr(strings.TrimSpace(in.Host))
	if err := cs.requireHost(host); err != nil {
		return nil, openFileOut{}, err
	}
	be, err := openFileBackend(host)
	if err != nil {
		return nil, openFileOut{}, err
	}
	// Expanded against THAT host's home, like every sidebar endpoint: a remote
	// ~/notes.md is not the lasso box's ~/notes.md.
	p = filepath.Clean(expandTildeOn(be, p))
	if !filepath.IsAbs(p) {
		return nil, openFileOut{}, fmt.Errorf("could not expand %q on host %q", in.Path, mcpHostName(host))
	}
	info, err := be.Stat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, openFileOut{}, fmt.Errorf("%s does not exist on host %q — write the file before asking lasso to open it", p, mcpHostName(host))
		}
		return nil, openFileOut{}, fmt.Errorf("stat %s on host %q: %w", p, mcpHostName(host), err)
	}

	from, detail := "an agent", ""
	if strings.TrimSpace(in.PaneID) != "" {
		// Attribution only: an unresolvable pane still opens the file, and the
		// reason rides back in detail for the agent to fix next time.
		who, err := resolveCallerAgent(ctx, cs, "", in.PaneID)
		switch {
		case err != nil:
			detail = "could not name you: " + err.Error()
		case who.Found:
			if name := firstNonEmpty(who.Agent.SidebarName, who.Agent.Title); name != "" {
				from = name
			}
		default:
			detail = "could not name you: " + who.Detail
		}
	}

	ev := openFileEvent{Path: p, Host: be.Name(), From: from, Dir: info.IsDir(), App: app}
	if !ev.Dir {
		ev.Line = in.Line
	}
	out := openFileOut{Path: p, Host: mcpHostName(ev.Host), Dir: ev.Dir}
	out.Web, out.Mac = openFileBroadcast(ev)
	out.Delivered = out.Web + out.Mac
	if out.Delivered == 0 {
		// The honesty rule notify's `sent` follows: an agent must be able to tell
		// "it is on their screen" from "nobody was looking".
		out.Detail = openFileNobody(app)
		return nil, out, nil
	}
	out.Detail = detail
	return nil, out, nil
}

// ---------------------------------------------------------------------------
// lasso open — the same call from a shell
// ---------------------------------------------------------------------------

const openCLITimeout = 30 * time.Second

func printOpenUsage(w *os.File) {
	fmt.Fprint(w, `lasso open — show a file to the human, in the lasso web app's file viewer or the Lasso Mac app

usage:
  lasso open -app web|mac [flags] <path> [flags]

flags:
  -line <n>       scroll to (and select) this 1-based line
  -host <alias>   host the file lives on (default: your own host — see below)
  -pane <id>      your herdr pane id, to say who opened it (default: $HERDR_PANE_ID)
  -app <app>      required: web (the web app's sidebar viewer) or mac (a window
                  in the Lasso Mac app)

<path> may be relative: it is resolved against THIS shell's working directory,
on the machine this command runs on, before the call. A ~/ path is passed
through and expanded against the target host's home. A directory opens the
sidebar's file tree there instead of the viewer.

The host defaults to the caller's own host, which for a CLI with no per-host
credential (no LASSO_MCP_TOKEN) is lasso's own box, "local". Running this on a
remote fleet machine without a per-host credential? Pass -host <alias>, or the
path will be looked for on the lasso host instead.

Exits non-zero when the app it was meant for is not open (nobody saw it), with the reason on
stderr — so an agent can tell "on their screen" from "nobody was looking".

environment:
  LASSO_LISTEN      host:port of the local lasso (default `+defaultListenAddr+`)
  LASSO_URL         full base URL, if lasso is not on plain http loopback
  LASSO_MCP_TOKEN   bearer token, when /mcp is gated by MCP_OAUTH
  UI_AUTH           user:pass, when the server runs behind basic auth
`)
}

func cliOpen(args []string) {
	in, err := parseOpenArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		printOpenUsage(os.Stdout)
		return
	}
	if err != nil {
		if !errors.Is(err, errFlagReported) {
			fmt.Fprintf(os.Stderr, "lasso open: %v\n", err)
			printOpenUsage(os.Stderr)
		}
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), openCLITimeout)
	defer cancel()
	out, err := callOpenFileTool(ctx, mcpEndpoint(), mcpCLIClient(), in)
	if err != nil {
		fatal("open: %v", err)
	}
	if out.Delivered == 0 {
		fmt.Fprintf(os.Stderr, "lasso open: not shown — %s\n", out.Detail)
		os.Exit(1)
	}
	what := "file"
	if out.Dir {
		what = "directory"
	}
	var where []string
	if out.Web > 0 {
		where = append(where, fmt.Sprintf("%d lasso tab%s", out.Web, plural(out.Web)))
	}
	if out.Mac > 0 {
		where = append(where, fmt.Sprintf("%d Mac app%s", out.Mac, plural(out.Mac)))
	}
	line := fmt.Sprintf("opened %s %s on %s in %s", what, out.Path, out.Host, strings.Join(where, " and "))
	if out.Detail != "" {
		line += " (" + out.Detail + ")"
	}
	fmt.Println(line)
}

// parseOpenArgs parses `lasso open`'s flags — before AND after the path, since
// `lasso open notes.md -line 40` is how people type it and the flag package
// stops at the first positional — and resolves a relative path against the
// working directory. Split from cliOpen so a test can drive it.
func parseOpenArgs(args []string) (openFileIn, error) {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}
	line := fs.Int("line", 0, "1-based line to scroll to")
	host := fs.String("host", "", "host the file lives on")
	pane := fs.String("pane", os.Getenv("HERDR_PANE_ID"), "your herdr pane id")
	app := fs.String("app", "", "web or mac (required)")
	parse := func(a []string) error {
		if err := fs.Parse(a); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return err
			}
			return errFlagReported
		}
		return nil
	}
	if err := parse(args); err != nil {
		return openFileIn{}, err
	}
	var paths []string
	for fs.NArg() > 0 {
		paths = append(paths, fs.Arg(0))
		if err := parse(fs.Args()[1:]); err != nil {
			return openFileIn{}, err
		}
	}
	switch len(paths) {
	case 0:
		return openFileIn{}, errors.New("no path given")
	case 1:
	default:
		return openFileIn{}, fmt.Errorf("one path at a time (got %d)", len(paths))
	}
	p, err := resolveCLIPath(paths[0])
	if err != nil {
		return openFileIn{}, err
	}
	if _, err := openFileApp(*app); err != nil {
		return openFileIn{}, err
	}
	return openFileIn{Path: p, Host: *host, Line: *line, PaneID: *pane, App: *app}, nil
}

// resolveCLIPath makes a relative path absolute against this process's working
// directory. A ~ path is left for the server, which expands it against the
// TARGET host's home rather than this machine's.
func resolveCLIPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p) {
		return p, nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", p, err)
	}
	return abs, nil
}

// callOpenFileTool connects to endpoint, calls open_file once and decodes its
// structured output — callNotifyTool's shape, so a test can drive the real
// /mcp handler over httptest.
func callOpenFileTool(ctx context.Context, endpoint string, hc *http.Client, in openFileIn) (openFileOut, error) {
	sess, err := dialMCP(ctx, endpoint, hc)
	if err != nil {
		return openFileOut{}, err
	}
	defer sess.Close()

	args := map[string]any{"path": in.Path}
	if in.Host != "" {
		args["host"] = in.Host
	}
	if in.Line > 0 {
		args["line"] = in.Line
	}
	if in.PaneID != "" {
		args["pane_id"] = in.PaneID
	}
	args["app"] = in.App
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "open_file", Arguments: args})
	if err != nil {
		return openFileOut{}, err
	}
	if res.IsError {
		return openFileOut{}, errors.New(toolErrorText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return openFileOut{}, fmt.Errorf("tool result: %w", err)
	}
	var out openFileOut
	if err := json.Unmarshal(raw, &out); err != nil {
		return openFileOut{}, fmt.Errorf("tool result: %w", err)
	}
	return out, nil
}
