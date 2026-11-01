package licensesbad

import "github.com/hashicorp/go-version"

func Parse(s string) (*version.Version, error) { return version.NewVersion(s) }
