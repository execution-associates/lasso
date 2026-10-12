package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The JSON shapes the Mac app (mac/) decodes, as golden files under
// mac/Spec/contract. Every field is filled with a non-zero value, so every
// tag (omitempty ones included) is in the file; the Swift tests decode these
// files with the app's own models. A renamed or removed tag therefore fails
// here first (the golden no longer matches) and then in Swift (a required key
// is missing). Regenerate with LASSO_UPDATE_CONTRACT=1 go test -run TestDesktopContract.
func TestDesktopContract(t *testing.T) {
	filled := func(v any) any {
		fillNonZero(reflect.ValueOf(v).Elem())
		return v
	}
	// botView embeds an unexported *botRecord, which reflection cannot set:
	// fill the record on its own and attach it.
	bot := filled(&botView{}).(*botView)
	bot.botRecord = filled(&botRecord{}).(*botRecord)
	samples := map[string]any{
		"host_pane":    filled(&hostPane{}),
		"bot_view":     bot,
		"chat_payload": filled(&chatPayload{}),
		"ui_state":     filled(&uiState{}),
		"hosts":        filled(&hostsPayload{}),
		// The SSE "notice" frame's data, and /api/version.
		"notice":  filled(&notice{}),
		"version": filled(&versionInfo{}),
		// POST /api/chat/answer's body: what the app sends, so a renamed key
		// fails here as surely as one the app reads.
		"chat_answer_request": filled(&chatAnswerRequest{}),
	}
	dir := filepath.Join("..", "mac", "Spec", "contract")
	update := os.Getenv("LASSO_UPDATE_CONTRACT") == "1"
	for name, v := range samples {
		got, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got = append(got, '\n')
		path := filepath.Join(dir, name+".json")
		if update {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with LASSO_UPDATE_CONTRACT=1)", name, err)
		}
		if string(want) != string(got) {
			t.Errorf("%s changed shape; update the Swift models in mac/ and regenerate %s with LASSO_UPDATE_CONTRACT=1", name, path)
		}
	}
}

var contractTime = time.Date(2026, 10, 11, 19, 0, 0, 0, time.UTC)

// fillNonZero sets every exported field reachable from v to a fixed non-zero
// value: strings "x", numbers 1, bools true, one-element slices and maps.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillNonZero(v.Elem())
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(contractTime))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillNonZero(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Slice:
		if v.Type() == reflect.TypeOf(json.RawMessage{}) {
			v.Set(reflect.ValueOf(json.RawMessage(`{}`)))
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillNonZero(k)
		e := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(e)
		m.SetMapIndex(k, e)
		v.Set(m)
	}
}
