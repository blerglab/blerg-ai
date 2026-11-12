package boardfiling

import (
	"encoding/json"
	"testing"
)

func TestMetadataBindsOmissionsTargetAndEveryField(t *testing.T) {
	name, desc, key := "Name", "Description", "KEY"
	d := MetadataPatch{BoardID: "11111111-1111-4111-8111-111111111111", Name: &name, Description: &desc, Key: &key}
	if err := ValidateMetadataPatch(d); err != nil {
		t.Fatal(err)
	}
	baseline := MetadataDigest(d)
	variants := []MetadataPatch{d, d, d, d}
	variants[0].Name = nil
	variants[1].Description = nil
	variants[2].Key = nil
	variants[3].BoardID = "22222222-2222-4222-8222-222222222222"
	for _, v := range variants {
		if MetadataDigest(v) == baseline {
			t.Fatal("field not bound")
		}
	}
	c, err := MetadataCapability(d.BoardID, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ParseMetadataCapability([]string{c}); !ok {
		t.Fatal("round trip")
	}
	for _, caps := range [][]string{{c, "board.admin"}, {"board.admin"}, {c + ":extra"}} {
		if _, _, ok := ParseMetadataCapability(caps); ok {
			t.Fatal("broader capability")
		}
	}
	empty := ""
	d.Description = &empty
	if err := ValidateMetadataPatch(d); err != nil {
		t.Fatal("cannot clear description")
	}
	d.Name = &empty
	if ValidateMetadataPatch(d) == nil {
		t.Fatal("empty name accepted")
	}
}

func TestMetadataConfigurationFields(t *testing.T) {
	for _, raw := range []string{
		`{"repos":[]}`, `{"require_repo":false}`, `{"deploy_url":""}`, `{"include_component_version":false}`,
		`{"columns":[{"name":"Review","is_terminal":false}]}`,
		`{"tags":[{"from":"old","to":"new"}]}`,
	} {
		var d MetadataPatch
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		d.BoardID = "11111111-1111-4111-8111-111111111111"
		if err := ValidateMetadataPatch(d); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
		if MetadataDigest(d) == MetadataDigest(MetadataPatch{BoardID: d.BoardID}) {
			t.Errorf("unbound: %s", raw)
		}
	}
}

func TestMetadataConfigurationRejectsAmbiguousChanges(t *testing.T) {
	for _, raw := range []string{
		`{"columns":[{"delete":true}]}`,
		`{"columns":[{"id":"11111111-1111-4111-8111-111111111111"}]}`,
		`{"columns":[{"id":"11111111-1111-4111-8111-111111111111","before_id":"11111111-1111-4111-8111-111111111111"}]}`,
		`{"columns":[{"name":""}]}`,
		`{"tags":[{"from":"old"}]}`,
		`{"tags":[{"from":"old","to":"new","delete":true}]}`,
		`{"tags":[{"from":"old","delete":true},{"from":"old","to":"new"}]}`,
		`{"field_schema":{}}`,
		`{"deploy_url":"javascript:alert(1)"}`,
		`{"repos":["bad/repo/path"]}`,
	} {
		var d MetadataPatch
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		d.BoardID = "11111111-1111-4111-8111-111111111111"
		if ValidateMetadataPatch(d) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
