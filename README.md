# Github License Fetcher

Github License Fetcher is a small CLI to retrieve licenses and other few metadata from github repositories based on the repository URL and version (tag or commit hash).
Licenses will be retrieved at the specific version and downloaded in the current directory.

## Usage
The CLI can run in 2 modes: single use or "batch".

### Single use
Run with either a repository URL and a version:
```bash
$ ./github-license-getter -r https://github.com/${OWNER}/${REPOSITORY}/ -t ${VERSION}
```
It will print the package metadata and license if found.

### Batch
Batch mode read a json file, consisting of an array of type `PackageSummary` (fields `repository` and `version` are mandatory):
```bash
$ ./github-license-getter -f ${FILE}
```
Packages parameters will be updated and a new file will be written with the datetime appended in the current directory.

Note: be careful of github rate limiting. Use the `-d` flag to specify a higher delay between 2 packages processing if needed. If a rate limit error has been raised, no more processing will be done.

### Download licenses only

Combined with batch mode above, it will only download license files where there is a `licenseURL`, but the corresponding `licenseTxt` is missing.
This works for all repositories, github and others as long as the URL is valid.
```bash
$ ./github-license-getter -f ${FILE} -l
```
Packages license texts will be updated and a new file will be written with the datetime appended in the current directory.

Note: URLs for `github.com` are automatically replaced with `raw.githubusercontent.com`.

### Authentication

A Github Token can be used, by setting it in the `GITHUB_TOKEN` variable name.

## Build

Just run `go build` to build the binary.