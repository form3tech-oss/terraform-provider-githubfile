// Copyright 2019 Form3 Financial Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package githubfile

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"githubfile": providerserver.NewProtocol6WithError(New()),
}

func testAccPreCheck(t *testing.T) {
	required := []string{
		"GITHUB_EMAIL",
		"GITHUB_TOKEN",
		"GITHUB_USERNAME",
	}
	for _, req := range required {
		if v := os.Getenv(req); v == "" {
			t.Fatalf("%q must be set for acceptance tests", req)
		}
	}
}

func TestProvider(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("provider should not be nil")
	}
}
