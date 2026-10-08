package credentials

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// bcryptHash is the 60-byte form that bcrypt.GenerateFromPassword returns. bcrypt.Cost alone
// accepts any longer text after a valid prefix, which could carry a plain password.
var bcryptHash = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)

func validApprovalHash(hash string) bool {
	_, err := bcrypt.Cost([]byte(hash))
	return err == nil && bcryptHash.MatchString(hash)
}

func validateRecord(r Record) error {
	if r.Version != 1 {
		return ErrUnsupportedVersion
	}
	if r.OAuth == nil || (r.ApprovalHash != "" && !validApprovalHash(r.ApprovalHash)) {
		return ErrInvalidRecord
	}
	for key, token := range r.OAuth {
		provider, account, found := strings.Cut(key, "/")
		if !found {
			return ErrInvalidRecord
		}
		if _, err := entry(provider, account); err != nil {
			return ErrInvalidRecord
		}
		if token.AccessToken == "" && token.RefreshToken == "" {
			return ErrInvalidRecord
		}
	}
	return nil
}
func decodeRecord(value []byte) (Record, error) {
	if !utf8.Valid(value) {
		return Record{}, ErrInvalidRecord
	}
	d := json.NewDecoder(bytes.NewReader(value))
	if uniqueJSON(d, 0) != nil {
		return Record{}, ErrInvalidRecord
	}
	if _, err := d.Token(); err != io.EOF {
		return Record{}, ErrInvalidRecord
	}
	// encoding/json otherwise accepts case-insensitive aliases for struct fields.
	var fields map[string]json.RawMessage
	// approval_hash is optional, so records written before it existed still load.
	if json.Unmarshal(value, &fields) != nil {
		return Record{}, ErrInvalidRecord
	}
	_, hasHash := fields["approval_hash"]
	want := 3
	if hasHash {
		want = 4
	}
	if len(fields) != want {
		return Record{}, ErrInvalidRecord
	}
	for _, key := range []string{"version", "oauth", "openrouter_key"} {
		if _, ok := fields[key]; !ok {
			return Record{}, ErrInvalidRecord
		}
	}
	var wire struct {
		Version       *int                       `json:"version"`
		OAuth         map[string]json.RawMessage `json:"oauth"`
		OpenRouterKey *string                    `json:"openrouter_key"`
		ApprovalHash  *string                    `json:"approval_hash"`
	}
	d = json.NewDecoder(bytes.NewReader(value))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.Version == nil || wire.OpenRouterKey == nil || wire.OAuth == nil || (hasHash && (wire.ApprovalHash == nil || *wire.ApprovalHash == "")) {
		return Record{}, ErrInvalidRecord
	}
	r := Record{Version: *wire.Version, OAuth: make(map[string]OAuth, len(wire.OAuth)), OpenRouterKey: strings.TrimSpace(*wire.OpenRouterKey)}
	if wire.ApprovalHash != nil {
		r.ApprovalHash = *wire.ApprovalHash
	}
	for key, raw := range wire.OAuth {
		token, err := decodeOAuth(raw)
		if err != nil {
			return Record{}, err
		}
		r.OAuth[key] = token
	}
	if err := validateRecord(r); err != nil {
		return Record{}, err
	}
	return r, nil
}

func decodeOAuth(value []byte) (OAuth, error) {
	d := json.NewDecoder(bytes.NewReader(value))
	if !utf8.Valid(value) || uniqueJSON(d, 0) != nil {
		return OAuth{}, ErrInvalidRecord
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil || fields == nil {
		return OAuth{}, ErrInvalidRecord
	}
	for key, raw := range fields {
		switch key {
		case "access_token", "refresh_token", "expires_at", "username":
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return OAuth{}, ErrInvalidRecord
			}
		case "scopes": // A nil scope slice is encoded as null by legacy stores.
		default:
			return OAuth{}, ErrInvalidRecord
		}
	}
	var token OAuth
	if json.Unmarshal(value, &token) != nil || (token.AccessToken == "" && token.RefreshToken == "") {
		return OAuth{}, ErrInvalidRecord
	}
	return token, nil
}

// Reject duplicate keys (including escaped spellings) instead of silently dropping data.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalidRecord
	}
	t, err := d.Token()
	if err != nil {
		return ErrInvalidRecord
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			s, ok := key.(string)
			if err != nil || !ok || seen[s] {
				return ErrInvalidRecord
			}
			seen[s] = true
			if uniqueJSON(d, depth+1) != nil {
				return ErrInvalidRecord
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return ErrInvalidRecord
		}
	case '[':
		for d.More() {
			if uniqueJSON(d, depth+1) != nil {
				return ErrInvalidRecord
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return ErrInvalidRecord
		}
	default:
		return ErrInvalidRecord
	}
	return nil
}
