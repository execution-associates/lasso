package main

// A bot's environment is mise's: plain values and age-encrypted secrets in the
// bot folder's mise.toml, decrypted by mise in memory when `mise run bot`
// starts. Lasso drives the mise CLI for every read and write and never parses
// the TOML. A secret's value goes in on STDIN (never argv, where `ps` would
// show it) and never comes back out: listings carry mise's own "[redacted]".

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const botMiseTimeout = 60 * time.Second

// botRun runs `mise <args>` with dir as the working directory on b's host,
// through a login shell so mise is on PATH the way it is for the human.
// A var so tests can record the calls instead of running mise.
var botRun = func(b Backend, dir string, args []string, stdin []byte) ([]byte, error) {
	parts := []string{"cd", shellQuote(dir), "&&", "mise"}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	line := strings.Join(parts, " ")
	if rb, ok := b.(*remoteBackend); ok {
		return rb.runStdin(`${SHELL:-sh} -lc `+shellQuote(line), stdin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), botMiseTimeout)
	defer cancel()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(ctx, shell, "-lc", line)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			return out, fmt.Errorf("mise %s: %s", strings.Join(args, " "), lastLines(msg, 3))
		}
		return out, fmt.Errorf("mise %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func botMiseFile(dir string) string { return filepath.Join(dir, "mise.toml") }

// botMiseInit makes the folder's mise.toml usable for the task: experimental
// mode (age values need it) and mise's trust, without which `mise run bot`
// refuses the file. Trust goes first when the file already exists, since mise
// will not even edit an untrusted config.
func botMiseInit(b Backend, dir string) error {
	if _, err := b.Stat(botMiseFile(dir)); err == nil {
		if _, err := botRun(b, dir, []string{"trust", botMiseFile(dir)}, nil); err != nil {
			return err
		}
	}
	if _, err := botRun(b, dir, []string{"settings", "set", "--local", "experimental", "true"}, nil); err != nil {
		return botMiseHint(err)
	}
	_, err := botRun(b, dir, []string{"trust", botMiseFile(dir)}, nil)
	return err
}

// botMiseHint turns "mise: command not found" into what to do about it.
func botMiseHint(err error) error {
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "not found") && strings.Contains(low, "mise") {
		return fmt.Errorf("mise is not installed on this host; bots need it (https://mise.jdx.dev/getting-started.html): %w", err)
	}
	return err
}

type botEnvVar struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"` // plain values only; never a secret's
	Secret bool   `json:"secret"`
}

// botEnvList lists the variables the bot's own mise.toml sets. `mise set`
// prints every config in scope (global ones too), one "KEY VALUE SOURCE" row
// each, so only rows whose source is this folder's file are kept.
func botEnvList(b Backend, dir string) ([]botEnvVar, error) {
	if _, err := b.Stat(botMiseFile(dir)); err != nil {
		return []botEnvVar{}, nil
	}
	out, err := botRun(b, dir, []string{"set"}, nil)
	if err != nil {
		return nil, botMiseHint(err)
	}
	return parseMiseSet(string(out), botMiseFile(dir)), nil
}

func parseMiseSet(out, file string) []botEnvVar {
	vars := []botEnvVar{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(strings.TrimSpace(line), file) {
			continue
		}
		f := strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), file))
		if len(f) == 0 || !botEnvKeyRE.MatchString(f[0]) {
			continue
		}
		value := strings.Join(f[1:], " ")
		v := botEnvVar{Key: f[0], Secret: value == "[redacted]"}
		if !v.Secret {
			v.Value = value
		}
		vars = append(vars, v)
	}
	return vars
}

// botEnvSet writes one variable. A secret is age-encrypted by mise with the
// host's mise age key; its value rides stdin.
func botEnvSet(b Backend, dir, key, value string, secret bool) error {
	if !botEnvKeyRE.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name", key)
	}
	if hasControl(value) && !secret {
		return fmt.Errorf("a plain value cannot contain control characters; mark it secret")
	}
	if secret {
		if ok, _ := botAgeKeyPresent(b); !ok {
			return errBotNoAgeKey
		}
		_, err := botRun(b, dir, []string{"set", "--file", botMiseFile(dir), "--age-encrypt", "--stdin", key}, []byte(value))
		return err
	}
	_, err := botRun(b, dir, []string{"set", "--file", botMiseFile(dir), key + "=" + value}, nil)
	return err
}

func botEnvUnset(b Backend, dir, key string) error {
	if !botEnvKeyRE.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name", key)
	}
	_, err := botRun(b, dir, []string{"unset", "--file", botMiseFile(dir), key}, nil)
	return err
}

var errBotNoAgeKey = errors.New("this host has no mise age key (~/.config/mise/age.txt), so secrets cannot be encrypted; create one in the bot's Environment settings")

func botAgeKeyPath(b Backend) (string, error) {
	home, err := b.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "mise", "age.txt"), nil
}

func botAgeKeyPresent(b Backend) (bool, error) {
	p, err := botAgeKeyPath(b)
	if err != nil {
		return false, err
	}
	_, err = b.Stat(p)
	return err == nil, nil
}

// botAgeKeyCreate makes the host's mise age identity, only on a human's click.
// It refuses to replace one: every secret already encrypted to the old key
// would become unreadable.
func botAgeKeyCreate(b Backend) error {
	p, err := botAgeKeyPath(b)
	if err != nil {
		return err
	}
	if _, err := b.Stat(p); err == nil {
		return fmt.Errorf("%s already exists", p)
	}
	if err := b.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	home, _ := b.HomeDir()
	_, err = botRun(b, home, []string{"x", "age", "--", "age-keygen", "-o", p}, nil)
	return botMiseHint(err)
}
