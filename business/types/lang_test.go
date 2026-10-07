package types_test

import (
	"testing"

	"github.com/jroedel/reconcile/business/types"
)

func TestParseLang(t *testing.T) {
	for give, want := range map[string]types.Lang{
		"en": types.English, "ES": types.Spanish, " pt ": types.Portuguese,
	} {
		got, err := types.ParseLang(give)
		if err != nil || got != want {
			t.Errorf("ParseLang(%q) = %q, %v; want %q", give, got, err, want)
		}
	}

	for _, give := range []string{"", "de", "pt-BR", "english"} {
		if _, err := types.ParseLang(give); err == nil {
			t.Errorf("ParseLang(%q) succeeded", give)
		}
	}
}

func TestTranslatedIsEveryLanguageButEnglish(t *testing.T) {
	if len(types.Translated) != len(types.Langs)-1 {
		t.Fatalf("Translated has %d, Langs %d", len(types.Translated), len(types.Langs))
	}

	for _, l := range types.Translated {
		if l == types.English {
			t.Fatal("English is in Translated")
		}
	}
}
