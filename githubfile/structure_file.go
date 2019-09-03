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
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
)

type file struct {
	repositoryOwner string
	repositoryName  string
	branch          string
	path            string
	contents        string
}

func expandFile(d *schema.ResourceData) *file {
	f := &file{}
	switch d.Id() {
	case "":
		f.repositoryOwner = d.Get(repositoryOwnerAttributeName).(string)
		f.repositoryName = d.Get(repositoryNameAttributeName).(string)
		f.branch = d.Get(branchAttributeName).(string)
		f.path = d.Get(pathAttributeName).(string)
	default:
		// Support importing existing files.
		if rn, ro, b, p, err := parseFileID(d.Id()); err == nil {
			f.repositoryOwner, f.repositoryName, f.branch, f.path = rn, ro, b, p
		}
	}
	f.contents = d.Get(contentsAttributeName).(string)
	return f
}

func flattenFile(f *file, d *schema.ResourceData) error {
	if err := d.Set(repositoryOwnerAttributeName, f.repositoryOwner); err != nil {
		return err
	}
	if err := d.Set(repositoryNameAttributeName, f.repositoryName); err != nil {
		return err
	}
	if err := d.Set(branchAttributeName, f.branch); err != nil {
		return err
	}
	if err := d.Set(pathAttributeName, f.path); err != nil {
		return err
	}
	if err := d.Set(contentsAttributeName, f.contents); err != nil {
		return err
	}
	d.SetId(fmt.Sprintf("%s/%s:%s:%s", f.repositoryOwner, f.repositoryName, f.branch, f.path))
	return nil
}

func parseFileID(v string) (string, string, string, string, error) {
	p := strings.Split(v, ":")
	if len(p) != 3 {
		return "", "", "", "", fmt.Errorf("failed to parse %q as a file id", v)
	}
	r := strings.Split(p[0], "/")
	if len(r) != 2 {
		return "", "", "", "", fmt.Errorf("failed to parse %q as a file id", v)
	}
	return r[0], r[1], p[1], p[2], nil
}
