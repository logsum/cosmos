package manifest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultAgentPrivateKeyPath returns the canonical location for the local
// Ed25519 private key used to sign manifest permission blocks
// (~/.cosmos/agents.private.key).
func DefaultAgentPrivateKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cosmos", "agents.private.key")
}

// PermissionMode controls how a permission request is handled by policy.
type PermissionMode string

const (
	PermissionAllow         PermissionMode = "allow"
	PermissionDeny          PermissionMode = "deny"
	PermissionRequestOnce   PermissionMode = "request_once"
	PermissionRequestAlways PermissionMode = "request_always"
)

var supportedParamTypes = map[string]struct{}{
	"string":  {},
	"number":  {},
	"boolean": {},
	"object":  {},
	"array":   {},
	"integer": {},
	"null":    {},
}

var permissionSegmentPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Manifest defines the on-disk schema of cosmo.manifest.json.
type Manifest struct {
	Name                 string                    `json:"name"`
	Version              string                    `json:"version"`
	Description          string                    `json:"description,omitempty"`
	Entry                string                    `json:"entry"`
	Functions            []FunctionDef             `json:"functions"`
	Permissions          map[string]PermissionMode `json:"permissions"`
	Timeout              string                    `json:"timeout,omitempty"`
	PermissionsSignature string                    `json:"permissions_signature,omitempty"`

	ParsedPermissions []PermissionRule `json:"-"`
	TimeoutDuration   time.Duration    `json:"-"`
}

// FunctionDef describes one callable tool function from the manifest.
type FunctionDef struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Params      map[string]ParamDef `json:"params,omitempty"`
	Returns     ReturnDef           `json:"returns"`
}

// ParamDef describes a single parameter in a function definition.
type ParamDef struct {
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
}

// ReturnDef describes the return type metadata of a function.
type ReturnDef struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// PermissionKey is the parsed form of a permission key.
type PermissionKey struct {
	Raw       string
	Resource  string
	Action    string
	Target    string
	HasTarget bool
	HasGlob   bool
}

// PermissionRule stores a parsed permission key and its mode.
type PermissionRule struct {
	Key  PermissionKey
	Mode PermissionMode
}

// VerifyConfig controls signature enforcement and key trust during parse.
type VerifyConfig struct {
	RequirePermissionSignature bool
	TrustedPublicKeys          []ed25519.PublicKey
}

// EmbeddedTrustedPublicKeys is the default in-code trust set used for
// permissions signature verification when VerifyConfig.TrustedPublicKeys is
// empty. It must be populated before the first ParseManifest call and not
// modified afterwards (not safe for concurrent mutation). Tests should prefer
// VerifyConfig.TrustedPublicKeys instead.
var EmbeddedTrustedPublicKeys []ed25519.PublicKey

// ParseManifestFile reads and parses a manifest from disk.
func ParseManifestFile(path string, cfg VerifyConfig) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	return ParseManifest(data, cfg)
}

// ParseManifest parses, validates and verifies a manifest payload.
func ParseManifest(data []byte, cfg VerifyConfig) (Manifest, error) {
	manifest, err := decodeManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := verifyPermissionSignature(manifest, cfg); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ParsePermissionKey parses keys like "fs:read:./src/**".
func ParsePermissionKey(key string) (PermissionKey, error) {
	raw := strings.TrimSpace(key)
	if raw == "" {
		return PermissionKey{}, errors.New("permission key cannot be empty")
	}

	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 {
		return PermissionKey{}, fmt.Errorf("permission key %q must include resource and action", raw)
	}

	resource := strings.TrimSpace(parts[0])
	action := strings.TrimSpace(parts[1])
	if !permissionSegmentPattern.MatchString(resource) {
		return PermissionKey{}, fmt.Errorf("invalid permission resource %q", resource)
	}
	if !permissionSegmentPattern.MatchString(action) {
		return PermissionKey{}, fmt.Errorf("invalid permission action %q", action)
	}

	parsed := PermissionKey{
		Raw:      raw,
		Resource: resource,
		Action:   action,
	}

	if len(parts) == 3 {
		target := strings.TrimSpace(parts[2])
		if target == "" {
			return PermissionKey{}, fmt.Errorf("permission key %q has empty target", raw)
		}
		if err := validateTargetGlob(target); err != nil {
			return PermissionKey{}, fmt.Errorf("invalid permission key %q: %w", raw, err)
		}
		parsed.Target = target
		parsed.HasTarget = true
		parsed.HasGlob = hasGlobMeta(target)
	}

	return parsed, nil
}

// CanonicalPermissionsPayload returns deterministic JSON used for signatures.
func CanonicalPermissionsPayload(permissions map[string]PermissionMode) ([]byte, error) {
	keys := make([]string, 0, len(permissions))
	for key := range permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("marshal permission key %q: %w", key, err)
		}
		v, err := json.Marshal(string(permissions[key]))
		if err != nil {
			return nil, fmt.Errorf("marshal permission mode for %q: %w", key, err)
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// SignPermissions signs the canonical permission block with an Ed25519 key.
func SignPermissions(permissions map[string]PermissionMode, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("invalid ed25519 private key size")
	}
	payload, err := CanonicalPermissionsPayload(permissions)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privateKey, payload)
	return base64.StdEncoding.EncodeToString(signature), nil
}

func decodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest

	decoder := json.NewDecoder(bytes.NewReader(data))
	// Intentional: reject unknown/misspelled fields to prevent silent
	// permission escalation via injected keys that bypass validation.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest json: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("decode manifest json: trailing content")
		}
		return Manifest{}, fmt.Errorf("decode manifest json: %w", err)
	}

	return manifest, nil
}

func validateManifest(manifest *Manifest) error {
	if strings.TrimSpace(manifest.Name) == "" {
		return errors.New("manifest.name is required")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return errors.New("manifest.version is required")
	}
	if strings.TrimSpace(manifest.Entry) == "" {
		return errors.New("manifest.entry is required")
	}
	if len(manifest.Functions) == 0 {
		return errors.New("manifest.functions is required")
	}
	if len(manifest.Permissions) == 0 {
		return errors.New("manifest.permissions is required")
	}

	if err := validateFunctions(manifest.Functions); err != nil {
		return err
	}

	rules, err := validatePermissions(manifest.Permissions)
	if err != nil {
		return err
	}
	manifest.ParsedPermissions = rules

	if strings.TrimSpace(manifest.Timeout) != "" {
		duration, err := time.ParseDuration(manifest.Timeout)
		if err != nil {
			return fmt.Errorf("manifest.timeout is invalid: %w", err)
		}
		if duration <= 0 {
			return fmt.Errorf("manifest.timeout must be positive (got %s)", manifest.Timeout)
		}
		manifest.TimeoutDuration = duration
	}

	return nil
}

func validateFunctions(functions []FunctionDef) error {
	seen := make(map[string]struct{}, len(functions))

	for i, fn := range functions {
		indexLabel := fmt.Sprintf("manifest.functions[%d]", i)
		name := strings.TrimSpace(fn.Name)
		if name == "" {
			return fmt.Errorf("%s.name is required", indexLabel)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate function name %q", name)
		}
		seen[name] = struct{}{}

		for paramName, paramDef := range fn.Params {
			if strings.TrimSpace(paramName) == "" {
				return fmt.Errorf("%s.params contains an empty parameter name", indexLabel)
			}
			if err := validateParamType(paramDef.Type); err != nil {
				return fmt.Errorf("%s.params.%s.type: %w", indexLabel, paramName, err)
			}
		}

		if err := validateParamType(fn.Returns.Type); err != nil {
			return fmt.Errorf("%s.returns.type: %w", indexLabel, err)
		}
	}

	return nil
}

func validateParamType(kind string) error {
	t := strings.TrimSpace(kind)
	if t == "" {
		return errors.New("is required")
	}
	if _, ok := supportedParamTypes[t]; !ok {
		return fmt.Errorf("unsupported type %q", t)
	}
	return nil
}

func validatePermissions(permissions map[string]PermissionMode) ([]PermissionRule, error) {
	rules := make([]PermissionRule, 0, len(permissions))
	for key, mode := range permissions {
		if !mode.isValid() {
			return nil, fmt.Errorf("manifest.permissions[%q] has invalid mode %q", key, mode)
		}

		parsedKey, err := ParsePermissionKey(key)
		if err != nil {
			return nil, err
		}

		rules = append(rules, PermissionRule{Key: parsedKey, Mode: mode})
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Key.Raw < rules[j].Key.Raw
	})

	return rules, nil
}

func verifyPermissionSignature(manifest Manifest, cfg VerifyConfig) error {
	signatureText := strings.TrimSpace(manifest.PermissionsSignature)
	if signatureText == "" {
		if cfg.RequirePermissionSignature {
			return errors.New("manifest.permissions_signature is required")
		}
		return nil
	}

	trustedKeys := cfg.TrustedPublicKeys
	if len(trustedKeys) == 0 {
		trustedKeys = EmbeddedTrustedPublicKeys
	}
	if len(trustedKeys) == 0 {
		return errors.New("manifest.permissions_signature is present but no trusted public keys are configured")
	}

	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		return fmt.Errorf("manifest.permissions_signature must be base64: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return errors.New("manifest.permissions_signature has invalid size")
	}

	payload, err := CanonicalPermissionsPayload(manifest.Permissions)
	if err != nil {
		return fmt.Errorf("canonicalize permissions: %w", err)
	}

	for _, key := range trustedKeys {
		if len(key) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(key, payload, signature) {
			return nil
		}
	}

	return errors.New("manifest.permissions_signature verification failed")
}

func validateTargetGlob(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("target cannot be empty")
	}

	// path.Match does not understand "**". Reduce it to "*" for syntax checks
	// so bracket and escape validation still happens.
	sanitized := strings.ReplaceAll(target, "**", "*")
	if _, err := path.Match(sanitized, "probe"); err != nil {
		return err
	}

	return nil
}

func hasGlobMeta(target string) bool {
	return strings.ContainsAny(target, "*?[")
}

func (mode PermissionMode) isValid() bool {
	switch mode {
	case PermissionAllow, PermissionDeny, PermissionRequestOnce, PermissionRequestAlways:
		return true
	default:
		return false
	}
}
