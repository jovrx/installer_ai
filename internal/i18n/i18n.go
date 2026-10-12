// Package i18n provides the shared, local-only RemoteOps language catalogs and settings.
package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
)

// Language is one of the two supported locale identifiers.
type Language string

const (
	Chinese Language = "zh-CN"
	English Language = "en-US"
)

// Parse accepts exact locale identifiers, without aliases or normalization.
func Parse(value string) (Language, error) {
	switch Language(value) {
	case Chinese, English:
		return Language(value), nil
	default:
		return "", errors.New("unsupported language; expected zh-CN or en-US")
	}
}

//go:embed locales/*.json
var catalogFiles embed.FS

var catalogs = loadCatalogs()

func loadCatalogs() map[Language]map[string]string {
	result := make(map[Language]map[string]string, 2)
	for _, language := range []Language{Chinese, English} {
		body, err := catalogFiles.ReadFile("locales/" + string(language) + ".json")
		if err != nil {
			panic("i18n: missing embedded catalog")
		}
		var catalog map[string]string
		if err := json.Unmarshal(body, &catalog); err != nil {
			panic("i18n: invalid embedded catalog")
		}
		result[language] = catalog
	}
	return result
}

// Text formats a catalog entry with fmt-compatible arguments. Unknown locales
// use Chinese; missing keys use a safe generic message.
// Callers remain responsible for redacting and cleaning presentation arguments.
func Text(language Language, key string, args ...any) string {
	catalog, ok := catalogs[language]
	if !ok {
		catalog = catalogs[Chinese]
	}
	entry, ok := catalog[key]
	if !ok || entry == "" {
		return catalog["common.error.generic"]
	}
	return fmt.Sprintf(entry, args...)
}
