package nodeops

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageOperationsScopeAndConfirmation(t *testing.T) {
	imageID := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"delete immutable image", `{"action":"image.delete","image_id":"ID"}`, true},
		{"delete requires full ID", `{"action":"image.delete","image_id":"app:latest"}`, false},
		{"delete cannot force", `{"action":"image.delete","image_id":"ID","overwrite":true}`, false},
		{"delete cannot target tag", `{"action":"image.delete","image_id":"ID","image":"reg.example/app:latest"}`, false},
		{"delete cannot change scope", `{"action":"image.delete","image_id":"ID","target":"prod"}`, false},
		{"preview", `{"action":"image.tag-preview","image_id":"ID","image":"reg.example/team/app:release"}`, true},
		{"confirm free tag", `{"action":"image.tag","image_id":"ID","image":"reg.example/team/app:release","expected_image_id":""}`, true},
		{"missing expectation", `{"action":"image.tag","image_id":"ID","image":"reg.example/team/app:release"}`, false},
		{"explicit target tag", `{"action":"image.tag-preview","image_id":"ID","image":"reg.example/team/app"}`, false},
		{"tag not digest", `{"action":"image.tag-preview","image_id":"ID","image":"reg.example/team/app@ID"}`, false},
		{"no mutation field for preview", `{"action":"image.tag-preview","image_id":"ID","image":"reg.example/team/app:release","overwrite":true}`, false},
		{"push with shared registry", `{"action":"image.push","image_id":"ID","image":"reg.example/team/app:release","registry_id":"example"}`, true},
		{"push scope", `{"action":"image.push","image_id":"ID","image":"reg.example/team/app:release","registry_id":"example","project":"team","target":"prod"}`, true},
		{"push without scope", `{"action":"image.push","image_id":"ID","image":"reg.example/team/app:release"}`, false},
		{"push cannot retag", `{"action":"image.push","image_id":"ID","image":"reg.example/team/app:release","expected_image_id":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request Request
			if err := json.Unmarshal([]byte(strings.ReplaceAll(tc.body, "ID", imageID)), &request); err != nil {
				t.Fatal(err)
			}
			if err := request.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid = %v, error = %v", tc.valid, err)
			}
		})
	}
}
