package main

import "testing"

func TestVaultScope(t *testing.T) {
	t.Cleanup(func() { setVaultScope("") })

	setVaultScope(" /Projetos/Time\\ ")
	if docScope != "Projetos/Time/" || vaultFolder != "Projetos/Time/worktrees" {
		t.Fatalf("scope = %q, folder = %q", docScope, vaultFolder)
	}
	if got, err := normalizeWiki("[[Projetos/Time/docs/frente.md]]"); err != nil || got != "[[Projetos/Time/docs/frente]]" {
		t.Fatalf("dentro do escopo: %q, %v", got, err)
	}
	if _, err := normalizeWiki("Pessoal/nota"); err == nil {
		t.Fatal("nota fora do escopo deveria ser recusada")
	}

	setVaultScope("")
	if docScope != defaultVaultScope+"/" {
		t.Fatalf("padrão = %q", docScope)
	}

	for _, bad := range []string{"../fora", "C:/Vault", "a/[x]"} {
		c := &Config{Root: t.TempDir(), Vault: &VaultConfig{Path: t.TempDir(), Scope: bad}, Repos: []RepoConfig{{Key: "a", Dir: "a"}}}
		applyDefaults(c)
		if err := c.validate(); err == nil {
			t.Errorf("scope %q deveria ser inválido", bad)
		}
	}
}
