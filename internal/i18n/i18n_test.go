package i18n

import "testing"

func TestLangBATUTAWins(t *testing.T) {
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("BATUTA_LANG", "pt-BR")
	if Lang() != "pt" {
		t.Fatalf("got %q", Lang())
	}
	t.Setenv("BATUTA_LANG", "en")
	if Lang() != "en" {
		t.Fatalf("got %q", Lang())
	}
}

func TestLangFromLANG(t *testing.T) {
	t.Setenv("BATUTA_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "pt_BR.UTF-8")
	if Lang() != "pt" {
		t.Fatalf("got %q", Lang())
	}
	t.Setenv("LANG", "en_GB.UTF-8")
	if Lang() != "en" {
		t.Fatalf("got %q", Lang())
	}
}

func TestLangDefaultEnglish(t *testing.T) {
	t.Setenv("BATUTA_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	if Lang() != "en" {
		t.Fatalf("got %q", Lang())
	}
}

func TestTPortuguesePack(t *testing.T) {
	t.Setenv("BATUTA_LANG", "pt")
	if T("home") != "Início" {
		t.Fatalf("got %q", T("home"))
	}
	t.Setenv("BATUTA_LANG", "en")
	if T("home") != "Home" {
		t.Fatalf("got %q", T("home"))
	}
}
