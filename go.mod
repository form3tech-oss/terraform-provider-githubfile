module github.com/form3tech-oss/terraform-provider-githubfile

go 1.12

replace git.apache.org/thrift.git => github.com/apache/thrift v0.12.0

require (
	github.com/form3tech-oss/go-github-utils v0.0.0-20190902102904-6021576c7116
	github.com/google/go-github/v28 v28.0.1
	github.com/hashicorp/terraform v0.12.7
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45
)
