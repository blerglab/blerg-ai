package boardfiling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// MetadataPatch preserves omission separately from an explicit empty description.
// No board execution or automation settings can be carried in this approval.
type MetadataPatch struct {
	DeployURL               *string          `json:"deploy_url,omitempty"`
	IncludeComponentVersion *bool            `json:"include_component_version,omitempty"`
	BoardID                 string           `json:"board_id"`
	Name                    *string          `json:"name,omitempty"`
	Description             *string          `json:"description,omitempty"`
	Key                     *string          `json:"key,omitempty"`
	Repos                   *[]string        `json:"repos,omitempty"`
	RequireRepo             *bool            `json:"require_repo,omitempty"`
	FieldSchema             *json.RawMessage `json:"field_schema,omitempty"`
	Columns                 []ColumnPatch    `json:"columns,omitempty"`
	Tags                    []TagPatch       `json:"tags,omitempty"`
}

// ColumnPatch creates (no id), updates, reorders, or deletes an empty column.
// BeforeID is omitted to keep position, or empty to move to the end.
type ColumnPatch struct {
	ID         string  `json:"id,omitempty"`
	Name       *string `json:"name,omitempty"`
	IsTerminal *bool   `json:"is_terminal,omitempty"`
	BeforeID   *string `json:"before_id,omitempty"`
	Delete     bool    `json:"delete,omitempty"`
}

// TagPatch renames or removes a tag on all matching cards of this board.
type TagPatch struct {
	From   string  `json:"from"`
	To     *string `json:"to,omitempty"`
	Delete bool    `json:"delete,omitempty"`
}

var metadataKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)

func ValidateMetadataPatch(d MetadataPatch) error {
	if !intentPattern.MatchString(d.BoardID) || (d.Name == nil && d.Description == nil && d.Key == nil && d.DeployURL == nil && d.IncludeComponentVersion == nil && d.Repos == nil && d.RequireRepo == nil && d.FieldSchema == nil && len(d.Columns) == 0 && len(d.Tags) == 0) {
		return errors.New("name a board and at least one metadata field")
	}
	if d.Name != nil && !oneLine(*d.Name, MaxBoardNameRunes) {
		return errors.New("name must be one line of 1 to 100 characters")
	}
	if d.Description != nil {
		if err := ValidateBoardDraft(BoardDraft{Name: "Board", Description: *d.Description}); err != nil {
			return err
		}
	}
	if d.Key != nil && !metadataKeyPattern.MatchString(*d.Key) {
		return errors.New("key must be 2 to 5 uppercase letters or digits, starting with a letter")
	}

	if d.DeployURL != nil && *d.DeployURL != "" {
		u, err := url.Parse(*d.DeployURL)
		if err != nil || !oneLine(*d.DeployURL, 2000) || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("deploy_url must be an http or https URL without credentials, or empty")
		}
	}
	if d.Repos != nil {
		if err := ValidateBoardDraft(BoardDraft{Name: "Board", Repos: *d.Repos}); err != nil {
			return err
		}
	}
	if d.FieldSchema != nil {
		var fields []map[string]any
		if json.Unmarshal(*d.FieldSchema, &fields) != nil || fields == nil || len(fields) > 100 {
			return errors.New("field_schema must be an array of at most 100 field definitions")
		}
	}
	if len(d.Columns) > 20 || len(d.Tags) > 100 {
		return errors.New("at most 20 column changes and 100 tag changes")
	}
	for _, c := range d.Columns {
		if c.ID != "" && !intentPattern.MatchString(c.ID) {
			return errors.New("column id must be a UUID")
		}
		if c.BeforeID != nil && (*c.BeforeID != "" && !intentPattern.MatchString(*c.BeforeID) || c.ID != "" && *c.BeforeID == c.ID) {
			return errors.New("before_id must name another column or be empty")
		}
		if c.Name != nil && !oneLine(*c.Name, MaxBoardColumnRunes) {
			return errors.New("column name must be one line of 1 to 64 characters")
		}
		if c.Delete {
			if c.ID == "" || c.Name != nil || c.IsTerminal != nil || c.BeforeID != nil {
				return errors.New("column deletion takes only id and delete")
			}
		} else if c.ID == "" && c.Name == nil || c.ID != "" && c.Name == nil && c.IsTerminal == nil && c.BeforeID == nil {
			return errors.New("provide a column name or a column change")
		}
	}
	seen := map[string]bool{}
	for _, t := range d.Tags {
		if !oneLine(t.From, 100) || seen[t.From] || (t.Delete == (t.To != nil)) {
			return errors.New("tag changes need a unique from and either to or delete")
		}
		if t.To != nil && (!oneLine(*t.To, 100) || *t.To == t.From) {
			return errors.New("new tag must be a different one-line name of 1 to 100 characters")
		}
		seen[t.From] = true
	}
	return nil
}
func MetadataDigest(d MetadataPatch) string {
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

const MetadataCapabilityPrefix = "board.metadata_once:"

func MetadataCapability(intent, digest string) (string, error) {
	c, err := Capability(intent, digest)
	return strings.Replace(c, CapabilityPrefix, MetadataCapabilityPrefix, 1), err
}
func ParseMetadataCapability(caps []string) (string, string, bool) {
	if len(caps) != 1 || !strings.HasPrefix(caps[0], MetadataCapabilityPrefix) {
		return "", "", false
	}
	return ParseCapability([]string{strings.Replace(caps[0], MetadataCapabilityPrefix, CapabilityPrefix, 1)})
}
