package i18n

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseSupportedLanguagesOnly(t *testing.T) {
	for _, value := range []string{"zh-CN", "en-US"} {
		got, err := Parse(value)
		if err != nil || string(got) != value {
			t.Fatalf("Parse(%q) = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "en", "zh", "EN-us", "en-US ", "fr-FR", "\nen-US"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("accepted unsupported language %q", value)
		}
	}
}

func TestTextFormatsSelectedLanguage(t *testing.T) {
	if got := Text(English, "common.error.detail", "network unavailable"); got != "Error: network unavailable" {
		t.Fatal(got)
	}
	if got := Text(Chinese, "common.error.detail", "network unavailable"); got != "错误：network unavailable" {
		t.Fatal(got)
	}
	if got := Text(English, "task.progress", "download", 50); got != "download (50%)" {
		t.Fatal(got)
	}
}

func TestTextFallbackDoesNotLeakKeyOrArguments(t *testing.T) {
	for _, language := range []Language{Chinese, English, Language("invalid")} {
		for _, key := range []string{"private.secret", ""} {
			got := Text(language, key, "do-not-display", 42)
			if got == "" || strings.Contains(got, "do-not-display") || strings.Contains(got, "private.secret") || strings.Contains(got, "%!") {
				t.Fatalf("unsafe fallback: %q", got)
			}
		}
	}
	if got := Text(Language("invalid"), "common.action.cancel"); got != "取消" {
		t.Fatalf("unknown language should use Chinese: %q", got)
	}
}

func TestTextPreservesPrintfLikeArgumentContent(t *testing.T) {
	if got := Text(English, "common.error.detail", "raw %! external output"); got != "Error: raw %! external output" {
		t.Fatalf("argument content was altered: %q", got)
	}
}

func TestCatalogKeyAndPlaceholderParity(t *testing.T) {
	read := func(path string) map[string]string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var catalog map[string]string
		if err := json.Unmarshal(body, &catalog); err != nil {
			t.Fatal(err)
		}
		return catalog
	}
	zh, en := read("locales/zh-CN.json"), read("locales/en-US.json")
	if len(zh) == 0 || len(zh) != len(en) {
		t.Fatalf("catalog sizes: zh=%d en=%d", len(zh), len(en))
	}
	placeholders := regexp.MustCompile(`%(?:\[[0-9]+\])?[-+# 0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?(?:\[[0-9]+\])?[a-zA-Z%]`)
	for key, text := range zh {
		translated, ok := en[key]
		if !ok || text == "" || translated == "" {
			t.Errorf("missing or empty translation for %q", key)
			continue
		}
		if !reflect.DeepEqual(placeholders.FindAllString(text, -1), placeholders.FindAllString(translated, -1)) {
			t.Errorf("placeholder mismatch for %q: %q / %q", key, text, translated)
		}
	}
}
