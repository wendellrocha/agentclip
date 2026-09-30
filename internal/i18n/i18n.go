// Package i18n translates what AgentClip shows to a person: the help text, the
// output of the commands and the Companion dashboard. English is the source
// language and the default: a message is its own key, and a catalog maps it to
// another language. What a catalog lacks is shown in English, so a missing
// translation is never a broken screen. Technical errors stay in English on
// purpose, so they can be searched for.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	// English is the source language and the default.
	English = "en-US"
	// Portuguese is the Brazilian Portuguese catalog.
	Portuguese = "pt-BR"
	// Env selects the language of the command line and the installers.
	Env = "AGENTCLIP_LANG"
)

//go:embed catalogs/*.json
var catalogFiles embed.FS

var catalogs = map[string]map[string]string{}

func init() {
	for _, language := range []string{Portuguese} {
		data, err := catalogFiles.ReadFile("catalogs/" + language + ".json")
		if err != nil {
			panic("i18n: " + err.Error())
		}
		var messages map[string]string
		if err := json.Unmarshal(data, &messages); err != nil {
			panic("i18n: catalog " + language + ": " + err.Error())
		}
		catalogs[language] = messages
	}
}

// Languages lists the languages a person can choose, English first.
func Languages() []string { return []string{English, Portuguese} }

var current atomic.Value

func init() { current.Store(English) }

// Language is the language of the process.
func Language() string { return current.Load().(string) }

// SetLanguage chooses the language of the process. An unknown one is English.
func SetLanguage(tag string) {
	if language, ok := Supported(tag); ok {
		current.Store(language)
		return
	}
	current.Store(English)
}

// Init reads AGENTCLIP_LANG. It is the only thing that picks the language of the
// command line: the system locale is not consulted, so the same command prints
// the same words on every machine unless the person asks otherwise.
func Init() { SetLanguage(os.Getenv(Env)) }

// Supported normalizes a language tag such as "pt", "pt_BR.UTF-8" or "en-GB"
// to a language with a catalog, and says whether there was one.
func Supported(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if before, _, found := strings.Cut(tag, "."); found {
		tag = before
	}
	if before, _, found := strings.Cut(tag, "@"); found {
		tag = before
	}
	tag = strings.ToLower(strings.ReplaceAll(tag, "_", "-"))
	switch {
	case tag == "pt" || strings.HasPrefix(tag, "pt-"):
		return Portuguese, true
	case tag == "en" || strings.HasPrefix(tag, "en-"):
		return English, true
	}
	return "", false
}

// FromAcceptLanguage picks the supported language a browser prefers, or "" when
// it asks for none of them. Quality values are honoured; the position in the
// header breaks ties.
func FromAcceptLanguage(header string) string {
	type choice struct {
		language string
		quality  float64
		position int
	}
	var choices []choice
	for position, part := range strings.Split(header, ",") {
		tag, parameters, _ := strings.Cut(strings.TrimSpace(part), ";")
		quality := 1.0
		if value, found := strings.CutPrefix(strings.TrimSpace(parameters), "q="); found {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				quality = parsed
			}
		}
		if language, ok := Supported(tag); ok && quality > 0 {
			choices = append(choices, choice{language, quality, position})
		}
	}
	sort.SliceStable(choices, func(i, j int) bool {
		if choices[i].quality != choices[j].quality {
			return choices[i].quality > choices[j].quality
		}
		return choices[i].position < choices[j].position
	})
	if len(choices) == 0 {
		return ""
	}
	return choices[0].language
}

// T translates message into the language of the process, formatting it with args
// as fmt.Sprintf would. The English message is the key.
func T(message string, args ...any) string { return Tr(Language(), message, args...) }

// Tr is T for a given language.
func Tr(language, message string, args ...any) string {
	if translated, ok := catalogs[language][message]; ok && translated != "" {
		message = translated
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

// Catalog returns a copy of the translations for language: nothing for English,
// which needs none. The dashboard receives it to translate what it builds.
func Catalog(language string) map[string]string {
	copied := make(map[string]string, len(catalogs[language]))
	for key, value := range catalogs[language] {
		copied[key] = value
	}
	return copied
}
