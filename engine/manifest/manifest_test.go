package manifest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseManifestValid(t *testing.T) {
	pub, priv := testKeyPair()

	m := validManifest()
	signature, err := SignPermissions(m.Permissions, priv)
	if err != nil {
		t.Fatalf("sign permissions: %v", err)
	}
	m.PermissionsSignature = signature

	payload := mustJSON(t, m)
	parsed, err := ParseManifest(payload, VerifyConfig{
		RequirePermissionSignature: true,
		TrustedPublicKeys:          []ed25519.PublicKey{pub},
	})
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}

	if parsed.Name != "code-analyzer" {
		t.Fatalf("parsed.Name = %q, want code-analyzer", parsed.Name)
	}
	if parsed.TimeoutDuration != 30*time.Second {
		t.Fatalf("parsed.TimeoutDuration = %s, want 30s", parsed.TimeoutDuration)
	}
	if len(parsed.ParsedPermissions) != 2 {
		t.Fatalf("len(parsed.ParsedPermissions) = %d, want 2", len(parsed.ParsedPermissions))
	}

	var foundGlob bool
	for _, rule := range parsed.ParsedPermissions {
		if rule.Key.Raw == "fs:read:./src/**" {
			foundGlob = true
			if !rule.Key.HasGlob {
				t.Fatal("expected fs:read:./src/** to be parsed as glob permission")
			}
		}
	}
	if !foundGlob {
		t.Fatal("expected fs:read:./src/** permission rule")
	}
}

func TestParseManifestFile(t *testing.T) {
	m := validManifest()
	payload := mustJSON(t, m)

	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "cosmo.manifest.json")
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	parsed, err := ParseManifestFile(manifestPath, VerifyConfig{})
	if err != nil {
		t.Fatalf("ParseManifestFile() error = %v", err)
	}
	if parsed.Entry != "index.js" {
		t.Fatalf("parsed.Entry = %q, want index.js", parsed.Entry)
	}
}

func TestParseManifestRequiredFields(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Manifest)
		wantError string
	}{
		{
			name: "missing name",
			mutate: func(m *Manifest) {
				m.Name = ""
			},
			wantError: "manifest.name is required",
		},
		{
			name: "missing version",
			mutate: func(m *Manifest) {
				m.Version = ""
			},
			wantError: "manifest.version is required",
		},
		{
			name: "missing entry",
			mutate: func(m *Manifest) {
				m.Entry = ""
			},
			wantError: "manifest.entry is required",
		},
		{
			name: "missing functions",
			mutate: func(m *Manifest) {
				m.Functions = nil
			},
			wantError: "manifest.functions is required",
		},
		{
			name: "missing permissions",
			mutate: func(m *Manifest) {
				m.Permissions = nil
			},
			wantError: "manifest.permissions is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)

			_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
			if err == nil {
				t.Fatal("expected ParseManifest() to fail")
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %q, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestParseManifestFunctionValidation(t *testing.T) {
	t.Run("duplicate function names", func(t *testing.T) {
		m := validManifest()
		m.Functions = append(m.Functions, m.Functions[0])

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "duplicate function name") {
			t.Fatalf("error = %v, want duplicate function name", err)
		}
	})

	t.Run("invalid param type", func(t *testing.T) {
		m := validManifest()
		m.Functions[0].Params["filePath"] = ParamDef{Type: "float"}

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "unsupported type") {
			t.Fatalf("error = %v, want unsupported type", err)
		}
	})

	t.Run("missing returns type", func(t *testing.T) {
		m := validManifest()
		m.Functions[0].Returns = ReturnDef{}

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "returns.type") {
			t.Fatalf("error = %v, want returns.type error", err)
		}
	})
}

func TestParseManifestPermissionValidation(t *testing.T) {
	t.Run("invalid mode", func(t *testing.T) {
		m := validManifest()
		m.Permissions["fs:read:./src/**"] = PermissionMode("ask_every_time")

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "invalid mode") {
			t.Fatalf("error = %v, want invalid mode", err)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		m := validManifest()
		m.Permissions = map[string]PermissionMode{
			"fs::./src/**": PermissionAllow,
		}

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "invalid permission action") {
			t.Fatalf("error = %v, want invalid permission key error", err)
		}
	})
}

func TestParsePermissionKey(t *testing.T) {
	key, err := ParsePermissionKey("fs:read:./src/**")
	if err != nil {
		t.Fatalf("ParsePermissionKey() error = %v", err)
	}

	if key.Resource != "fs" || key.Action != "read" {
		t.Fatalf("unexpected resource/action: %s/%s", key.Resource, key.Action)
	}
	if key.Target != "./src/**" {
		t.Fatalf("key.Target = %q, want ./src/**", key.Target)
	}
	if !key.HasTarget || !key.HasGlob {
		t.Fatal("expected target/glob flags to be true")
	}
}

func TestParseManifestTimeout(t *testing.T) {
	t.Run("valid timeout", func(t *testing.T) {
		m := validManifest()
		m.Timeout = "45s"

		parsed, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err != nil {
			t.Fatalf("ParseManifest() error = %v", err)
		}
		if parsed.TimeoutDuration != 45*time.Second {
			t.Fatalf("TimeoutDuration = %s, want 45s", parsed.TimeoutDuration)
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		m := validManifest()
		m.Timeout = "45seconds"

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "manifest.timeout is invalid") {
			t.Fatalf("error = %v, want timeout parse error", err)
		}
	})

	t.Run("zero timeout", func(t *testing.T) {
		m := validManifest()
		m.Timeout = "0s"

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "manifest.timeout must be positive") {
			t.Fatalf("error = %v, want timeout must be positive error", err)
		}
	})

	t.Run("negative timeout", func(t *testing.T) {
		m := validManifest()
		m.Timeout = "-5s"

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{})
		if err == nil || !strings.Contains(err.Error(), "manifest.timeout must be positive") {
			t.Fatalf("error = %v, want timeout must be positive error", err)
		}
	})
}

func TestParseManifestUnknownField(t *testing.T) {
	raw := []byte(`{
		"name":"code-analyzer",
		"version":"1.0.0",
		"entry":"index.js",
		"functions":[{"name":"analyze","returns":{"type":"object"}}],
		"permissions":{"net:http":"allow"},
		"unexpected":true
	}`)

	_, err := ParseManifest(raw, VerifyConfig{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field error", err)
	}
}

func TestParseManifestEd25519Verification(t *testing.T) {
	trustedPub, trustedPriv := testKeyPair()
	otherPub, _ := testKeyPairFromSeed(7)

	t.Run("missing signature when required", func(t *testing.T) {
		m := validManifest()
		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
			TrustedPublicKeys:          []ed25519.PublicKey{trustedPub},
		})
		if err == nil || !strings.Contains(err.Error(), "manifest.permissions_signature is required") {
			t.Fatalf("error = %v, want missing signature error", err)
		}
	})

	t.Run("valid signature", func(t *testing.T) {
		m := validManifest()
		sig, err := SignPermissions(m.Permissions, trustedPriv)
		if err != nil {
			t.Fatalf("SignPermissions(): %v", err)
		}
		m.PermissionsSignature = sig

		_, err = ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
			TrustedPublicKeys:          []ed25519.PublicKey{trustedPub},
		})
		if err != nil {
			t.Fatalf("ParseManifest() error = %v", err)
		}
	})

	t.Run("tampered permissions", func(t *testing.T) {
		m := validManifest()
		sig, err := SignPermissions(m.Permissions, trustedPriv)
		if err != nil {
			t.Fatalf("SignPermissions(): %v", err)
		}
		m.PermissionsSignature = sig
		m.Permissions["net:http"] = PermissionDeny

		_, err = ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
			TrustedPublicKeys:          []ed25519.PublicKey{trustedPub},
		})
		if err == nil || !strings.Contains(err.Error(), "verification failed") {
			t.Fatalf("error = %v, want verification failure", err)
		}
	})

	t.Run("unknown trusted key", func(t *testing.T) {
		m := validManifest()
		sig, err := SignPermissions(m.Permissions, trustedPriv)
		if err != nil {
			t.Fatalf("SignPermissions(): %v", err)
		}
		m.PermissionsSignature = sig

		_, err = ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
			TrustedPublicKeys:          []ed25519.PublicKey{otherPub},
		})
		if err == nil || !strings.Contains(err.Error(), "verification failed") {
			t.Fatalf("error = %v, want verification failure", err)
		}
	})

	t.Run("invalid base64 signature", func(t *testing.T) {
		m := validManifest()
		m.PermissionsSignature = "not_base64@"

		_, err := ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
			TrustedPublicKeys:          []ed25519.PublicKey{trustedPub},
		})
		if err == nil || !strings.Contains(err.Error(), "must be base64") {
			t.Fatalf("error = %v, want base64 error", err)
		}
	})

	// This test mutates EmbeddedTrustedPublicKeys, which is safe only because
	// tests run sequentially within a package. Production code should populate
	// it once at startup and use VerifyConfig.TrustedPublicKeys instead.
	t.Run("uses embedded trusted keys fallback", func(t *testing.T) {
		m := validManifest()
		sig, err := SignPermissions(m.Permissions, trustedPriv)
		if err != nil {
			t.Fatalf("SignPermissions(): %v", err)
		}
		m.PermissionsSignature = sig

		originalKeys := EmbeddedTrustedPublicKeys
		EmbeddedTrustedPublicKeys = []ed25519.PublicKey{trustedPub}
		t.Cleanup(func() { EmbeddedTrustedPublicKeys = originalKeys })

		_, err = ParseManifest(mustJSON(t, m), VerifyConfig{
			RequirePermissionSignature: true,
		})
		if err != nil {
			t.Fatalf("ParseManifest() error = %v", err)
		}
	})
}

func TestCanonicalPermissionsPayloadDeterministic(t *testing.T) {
	p1 := map[string]PermissionMode{
		"net:http":         PermissionRequestOnce,
		"fs:read:./src/**": PermissionAllow,
	}
	p2 := map[string]PermissionMode{}
	p2["fs:read:./src/**"] = PermissionAllow
	p2["net:http"] = PermissionRequestOnce

	b1, err := CanonicalPermissionsPayload(p1)
	if err != nil {
		t.Fatalf("CanonicalPermissionsPayload(p1): %v", err)
	}
	b2, err := CanonicalPermissionsPayload(p2)
	if err != nil {
		t.Fatalf("CanonicalPermissionsPayload(p2): %v", err)
	}

	if !bytes.Equal(b1, b2) {
		t.Fatalf("canonical payload mismatch:\n1=%s\n2=%s", string(b1), string(b2))
	}
}

func validManifest() Manifest {
	return Manifest{
		Name:    "code-analyzer",
		Version: "1.0.0",
		Entry:   "index.js",
		Functions: []FunctionDef{
			{
				Name: "analyzeFile",
				Params: map[string]ParamDef{
					"filePath": {
						Type:     "string",
						Required: true,
					},
				},
				Returns: ReturnDef{Type: "object"},
			},
		},
		Permissions: map[string]PermissionMode{
			"fs:read:./src/**": PermissionAllow,
			"net:http":         PermissionRequestOnce,
		},
		Timeout: "30s",
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(): %v", err)
	}
	return payload
}

func testKeyPair() (ed25519.PublicKey, ed25519.PrivateKey) {
	return testKeyPairFromSeed(3)
}

func testKeyPairFromSeed(seedValue byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := bytes.Repeat([]byte{seedValue}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return publicKey, privateKey
}
